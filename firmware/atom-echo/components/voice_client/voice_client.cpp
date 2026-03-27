#include "voice_client.h"
#include "esphome/core/log.h"
#include "esphome/core/application.h"

#include "esp_http_client.h"
#include "esp_tls.h"
#include "esp_crt_bundle.h"

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include <algorithm>
#include <cstring>

namespace esphome {
namespace voice_client {

static const char *const TAG = "voice_client";

static const uint32_t SAMPLE_RATE = 16000;
static const uint32_t BYTES_PER_SEC = SAMPLE_RATE * 2;  // 16-bit mono

void VoiceClient::setup() {
  ESP_LOGI(TAG, "Voice client initialized (url: %s, max_record: %ds)", api_url_.c_str(), max_record_seconds_);

  // Register callback to receive microphone data into static buffer.
  this->mic_->add_data_callback([this](const std::vector<uint8_t> &data) {
    if (state_ != State::RECORDING) return;
    size_t pos = buf_pos_;
    size_t space = AUDIO_BUF_SIZE - pos;
    if (space == 0) return;
    size_t to_copy = std::min(data.size(), space);
    memcpy(audio_buf_ + pos, data.data(), to_copy);
    buf_pos_ = pos + to_copy;
  });

  set_state_(State::IDLE);
}

void VoiceClient::loop() {
  // Check recording timeout.
  if (state_ == State::RECORDING) {
    uint32_t elapsed_ms = millis() - record_start_;
    uint32_t max_ms = max_record_seconds_ * 1000;
    // Also stop if buffer is full.
    if (elapsed_ms >= max_ms || buf_pos_ >= AUDIO_BUF_SIZE) {
      ESP_LOGW(TAG, "Max recording time/buffer reached");
      stop_recording();
    }
  }

  if (should_process_) {
    should_process_ = false;
    // Run HTTP request in a separate FreeRTOS task to avoid watchdog timeout.
    xTaskCreate([](void *param) {
      auto *self = static_cast<VoiceClient *>(param);
      self->do_process_();
      vTaskDelete(nullptr);
    }, "voice_http", 8192, this, 5, nullptr);
  }
}

void VoiceClient::start_recording() {
  if (state_ != State::IDLE) {
    ESP_LOGW(TAG, "Cannot record: state=%d", (int)state_);
    return;
  }

  // Leave space for WAV header at the beginning.
  buf_pos_ = WAV_HEADER_SIZE;
  record_start_ = millis();
  set_state_(State::RECORDING);
  this->mic_->start();
  ESP_LOGI(TAG, "Recording started");
}

void VoiceClient::stop_recording() {
  if (state_ != State::RECORDING) return;

  this->mic_->stop();

  size_t data_size = buf_pos_ - WAV_HEADER_SIZE;
  uint32_t elapsed_ms = millis() - record_start_;
  ESP_LOGI(TAG, "Recording stopped: %u bytes, %u ms", (unsigned)data_size, elapsed_ms);

  if (data_size < BYTES_PER_SEC / 4) {
    ESP_LOGW(TAG, "Recording too short (%u bytes), ignoring", (unsigned)data_size);
    set_state_(State::IDLE);
    return;
  }

  build_wav_header_(audio_buf_, data_size);
  set_state_(State::PROCESSING);
  should_process_ = true;
}

void VoiceClient::do_process_() {
  size_t total_size = buf_pos_;
  ESP_LOGI(TAG, "Sending %u bytes to API...", (unsigned)total_size);

  std::string auth_header = "Bearer " + api_token_;

  esp_http_client_config_t config = {};
  config.url = api_url_.c_str();
  config.method = HTTP_METHOD_POST;
  config.timeout_ms = 30000;
  config.buffer_size = 2048;
  config.buffer_size_tx = 2048;
  config.crt_bundle_attach = esp_crt_bundle_attach;

  esp_http_client_handle_t client = esp_http_client_init(&config);
  if (!client) {
    ESP_LOGE(TAG, "HTTP client init failed");
    set_state_(State::ERROR);
    return;
  }

  esp_http_client_set_header(client, "Content-Type", "audio/wav");
  esp_http_client_set_header(client, "Authorization", auth_header.c_str());
  esp_http_client_set_header(client, "Accept", "audio/mpeg");

  // Use open/write/fetch/read instead of perform() to access the response body.
  esp_err_t err = esp_http_client_open(client, total_size);
  if (err != ESP_OK) {
    ESP_LOGE(TAG, "HTTP open failed: %s", esp_err_to_name(err));
    esp_http_client_cleanup(client);
    set_state_(State::ERROR);
    return;
  }

  // Write request body.
  int written = esp_http_client_write(client, (const char *)audio_buf_, total_size);
  if (written < 0) {
    ESP_LOGE(TAG, "HTTP write failed");
    esp_http_client_cleanup(client);
    set_state_(State::ERROR);
    return;
  }
  ESP_LOGI(TAG, "Sent %d bytes", written);

  // Read response headers.
  int content_length = esp_http_client_fetch_headers(client);
  int status = esp_http_client_get_status_code(client);
  ESP_LOGI(TAG, "HTTP response: status=%d, content_length=%d", status, content_length);

  if (status != 200) {
    ESP_LOGE(TAG, "API error: HTTP %d", status);
    esp_http_client_cleanup(client);
    set_state_(State::ERROR);
    return;
  }

  // Read response body into audio_buf_ (reuse recording buffer).
  size_t total_read = 0;
  while (total_read < AUDIO_BUF_SIZE) {
    int read_len = esp_http_client_read(client, (char *)audio_buf_ + total_read, AUDIO_BUF_SIZE - total_read);
    if (read_len <= 0) break;
    total_read += read_len;
  }
  esp_http_client_close(client);
  esp_http_client_cleanup(client);

  if (total_read == 0) {
    ESP_LOGE(TAG, "No audio data in response");
    set_state_(State::ERROR);
    return;
  }
  ESP_LOGI(TAG, "Received %u bytes of audio", (unsigned)total_read);
  int read_len = total_read;

  // Play response.
  set_state_(State::PLAYING);
  this->spk_->start();
  this->spk_->play(audio_buf_, read_len);
  this->spk_->finish();
  set_state_(State::IDLE);
}

void VoiceClient::build_wav_header_(uint8_t *buf, uint32_t data_size) {
  uint32_t file_size = data_size + 36;
  uint16_t num_channels = 1;
  uint16_t bits_per_sample = 16;
  uint32_t byte_rate = SAMPLE_RATE * num_channels * bits_per_sample / 8;
  uint16_t block_align = num_channels * bits_per_sample / 8;

  auto write16 = [&](size_t offset, uint16_t v) {
    buf[offset] = v & 0xFF;
    buf[offset + 1] = (v >> 8) & 0xFF;
  };
  auto write32 = [&](size_t offset, uint32_t v) {
    buf[offset] = v & 0xFF;
    buf[offset + 1] = (v >> 8) & 0xFF;
    buf[offset + 2] = (v >> 16) & 0xFF;
    buf[offset + 3] = (v >> 24) & 0xFF;
  };

  memcpy(buf, "RIFF", 4);
  write32(4, file_size);
  memcpy(buf + 8, "WAVE", 4);
  memcpy(buf + 12, "fmt ", 4);
  write32(16, 16);
  write16(20, 1);  // PCM
  write16(22, num_channels);
  write32(24, SAMPLE_RATE);
  write32(28, byte_rate);
  write16(32, block_align);
  write16(34, bits_per_sample);
  memcpy(buf + 36, "data", 4);
  write32(40, data_size);
}

void VoiceClient::set_state_(State state) {
  state_ = state;
  switch (state) {
    case State::IDLE:
      set_led_color_(0, 0, 1.0f, 0.2f);
      break;
    case State::RECORDING:
      set_led_color_(1.0f, 0, 0);
      break;
    case State::PROCESSING:
      set_led_pulse_();
      break;
    case State::PLAYING:
      set_led_color_(0, 1.0f, 0);
      break;
    case State::ERROR:
      for (int i = 0; i < 3; i++) {
        set_led_color_(1.0f, 0, 0, 1.0f);
        delay(150);
        set_led_color_(0, 0, 0, 0);
        delay(150);
      }
      set_state_(State::IDLE);
      break;
  }
}

void VoiceClient::set_led_color_(float r, float g, float b, float brightness) {
  if (!led_) return;
  auto call = led_->turn_on();
  call.set_rgb(r, g, b);
  call.set_brightness(brightness);
  call.set_transition_length(0);
  call.perform();
}

void VoiceClient::set_led_pulse_() {
  if (!led_) return;
  auto call = led_->turn_on();
  call.set_rgb(1.0f, 0.7f, 0);
  call.set_brightness(0.8f);
  call.set_effect("Pulse");
  call.perform();
}

}  // namespace voice_client
}  // namespace esphome
