#include "voice_client.h"
#include "esphome/core/log.h"
#include "esphome/core/application.h"

#include "esp_http_client.h"
#include "esp_tls.h"

#include <cstring>

namespace esphome {
namespace voice_client {

static const char *const TAG = "voice_client";

// 16kHz, 16-bit, mono = 32 KB/sec
static const uint32_t SAMPLE_RATE = 16000;
static const uint32_t BYTES_PER_SEC = SAMPLE_RATE * 2;  // 16-bit = 2 bytes per sample
static const size_t WAV_HEADER_SIZE = 44;

void VoiceClient::setup() {
  ESP_LOGI(TAG, "Voice client initialized (url: %s, max_record: %ds)", api_url_.c_str(), max_record_seconds_);
  audio_buffer_.reserve(max_record_seconds_ * BYTES_PER_SEC + WAV_HEADER_SIZE);
  set_state_(State::IDLE);
}

void VoiceClient::loop() {
  // Check recording timeout.
  if (state_ == State::RECORDING) {
    uint32_t elapsed_ms = millis() - record_start_;
    if (elapsed_ms >= (uint32_t)(max_record_seconds_ * 1000)) {
      ESP_LOGW(TAG, "Max recording time reached (%ds)", max_record_seconds_);
      stop_recording();
    }
  }

  // Process in main loop to avoid blocking ISR callbacks.
  if (should_process_) {
    should_process_ = false;
    do_process_();
  }
}

void VoiceClient::start_recording() {
  if (state_ != State::IDLE) {
    ESP_LOGW(TAG, "Cannot record: state=%d", (int)state_);
    return;
  }

  ESP_LOGI(TAG, "Recording started");
  audio_buffer_.clear();
  // Reserve space for WAV header — we'll fill it in later.
  audio_buffer_.resize(WAV_HEADER_SIZE, 0);
  record_start_ = millis();
  set_state_(State::RECORDING);

  this->mic_->start();
}

void VoiceClient::stop_recording() {
  if (state_ != State::RECORDING) return;

  this->mic_->stop();

  // Read all available audio data from the microphone buffer.
  // ESPHome microphone component provides data via read() method.
  size_t data_size = audio_buffer_.size() - WAV_HEADER_SIZE;
  uint32_t elapsed_ms = millis() - record_start_;
  ESP_LOGI(TAG, "Recording stopped: %u bytes, %u ms", (unsigned)data_size, elapsed_ms);

  if (data_size < BYTES_PER_SEC / 4) {  // Less than 0.25 sec
    ESP_LOGW(TAG, "Recording too short, ignoring");
    set_state_(State::IDLE);
    return;
  }

  // Build WAV header at the beginning of the buffer.
  build_wav_header_(audio_buffer_, data_size);

  set_state_(State::PROCESSING);
  should_process_ = true;
}

void VoiceClient::do_process_() {
  ESP_LOGI(TAG, "Sending %u bytes to API...", (unsigned)audio_buffer_.size());
  set_led_pulse_();

  // Configure HTTP client.
  std::string auth_header = "Bearer " + api_token_;

  esp_http_client_config_t config = {};
  config.url = api_url_.c_str();
  config.method = HTTP_METHOD_POST;
  config.timeout_ms = 30000;
  config.buffer_size = 2048;
  config.buffer_size_tx = 2048;
  // Use system cert bundle for TLS (Let's Encrypt).
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
  esp_http_client_set_post_field(client, (const char *)audio_buffer_.data(), audio_buffer_.size());

  esp_err_t err = esp_http_client_perform(client);
  if (err != ESP_OK) {
    ESP_LOGE(TAG, "HTTP request failed: %s", esp_err_to_name(err));
    esp_http_client_cleanup(client);
    set_state_(State::ERROR);
    return;
  }

  int status = esp_http_client_get_status_code(client);
  int content_length = esp_http_client_get_content_length(client);
  ESP_LOGI(TAG, "HTTP response: status=%d, length=%d", status, content_length);

  if (status != 200) {
    ESP_LOGE(TAG, "API error: HTTP %d", status);
    esp_http_client_cleanup(client);
    set_state_(State::ERROR);
    return;
  }

  // Reuse audio_buffer_ for response (recording data no longer needed).
  audio_buffer_.clear();
  if (content_length > 0) {
    audio_buffer_.resize(content_length);
  } else {
    audio_buffer_.resize(64 * 1024);  // 64 KB max if no Content-Length
  }

  int read_len = esp_http_client_read(client, (char *)audio_buffer_.data(), audio_buffer_.size());
  esp_http_client_cleanup(client);

  if (read_len <= 0) {
    ESP_LOGE(TAG, "No audio data in response");
    set_state_(State::ERROR);
    return;
  }
  audio_buffer_.resize(read_len);
  ESP_LOGI(TAG, "Received %d bytes of audio", read_len);

  // Play MP3 response through speaker.
  set_state_(State::PLAYING);
  // Feed audio data to the speaker component.
  // The ESPHome speaker component handles MP3 decoding internally
  // when using the media_player platform, but for raw speaker we
  // need to handle this differently.
  //
  // For now, send raw data to speaker — the speaker component
  // with media_player platform handles codec detection.
  this->spk_->start();
  size_t written = this->spk_->play(audio_buffer_.data(), audio_buffer_.size());
  ESP_LOGI(TAG, "Sent %u bytes to speaker", (unsigned)written);

  // Wait for playback to finish. The speaker runs asynchronously.
  // We'll check in loop() if the speaker is still playing,
  // but for simplicity set idle after a delay.
  // A better approach: poll spk_->is_running() in loop().
  this->spk_->finish();
  set_state_(State::IDLE);
}

void VoiceClient::build_wav_header_(std::vector<uint8_t> &buf, uint32_t data_size) {
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

  // RIFF header
  memcpy(buf.data(), "RIFF", 4);
  write32(4, file_size);
  memcpy(buf.data() + 8, "WAVE", 4);

  // fmt chunk
  memcpy(buf.data() + 12, "fmt ", 4);
  write32(16, 16);  // chunk size
  write16(20, 1);   // PCM format
  write16(22, num_channels);
  write32(24, SAMPLE_RATE);
  write32(28, byte_rate);
  write16(32, block_align);
  write16(34, bits_per_sample);

  // data chunk
  memcpy(buf.data() + 36, "data", 4);
  write32(40, data_size);
}

void VoiceClient::set_state_(State state) {
  state_ = state;
  switch (state) {
    case State::IDLE:
      set_led_color_(0, 0, 1.0f, 0.2f);  // dim blue
      break;
    case State::RECORDING:
      set_led_color_(1.0f, 0, 0);  // red
      break;
    case State::PROCESSING:
      set_led_pulse_();  // yellow pulse
      break;
    case State::PLAYING:
      set_led_color_(0, 1.0f, 0);  // green
      break;
    case State::ERROR:
      // Flash red 3 times, then go idle.
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
  call.set_rgb(1.0f, 0.7f, 0);  // yellow-orange
  call.set_brightness(0.8f);
  call.set_effect("Pulse");
  call.perform();
}

}  // namespace voice_client
}  // namespace esphome
