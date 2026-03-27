#pragma once

#include "esphome/core/component.h"
#include "esphome/components/microphone/microphone.h"
#include "esphome/components/speaker/speaker.h"
#include "esphome/components/light/light_state.h"

namespace esphome {
namespace voice_client {

enum class State : uint8_t {
  IDLE,
  RECORDING,
  PROCESSING,
  PLAYING,
  ERROR,
};

// Fixed audio buffer: 96 KB — used for both recording and playback (not simultaneously).
// Recording: ~3s at 16kHz 16bit mono. Playback: ~3s of WAV response.
static const size_t WAV_HEADER_SIZE = 44;
static const size_t AUDIO_BUF_SIZE = 96 * 1024;

class VoiceClient : public Component {
 public:
  void setup() override;
  void loop() override;
  float get_setup_priority() const override { return setup_priority::AFTER_WIFI; }

  void set_microphone(microphone::Microphone *mic) { this->mic_ = mic; }
  void set_speaker(speaker::Speaker *spk) { this->spk_ = spk; }
  void set_led(light::LightState *led) { this->led_ = led; }
  void set_api_url(const std::string &url) { this->api_url_ = url; }
  void set_api_token(const std::string &token) { this->api_token_ = token; }
  void set_max_record_seconds(int secs) { this->max_record_seconds_ = secs; }

  void start_recording();
  void stop_recording();

 protected:
  void set_state_(State state);
  void set_led_color_(float r, float g, float b, float brightness = 0.8f);
  void set_led_pulse_();
  void do_process_();
  void build_wav_header_(uint8_t *buf, uint32_t data_size);

  microphone::Microphone *mic_{nullptr};
  speaker::Speaker *spk_{nullptr};
  light::LightState *led_{nullptr};

  std::string api_url_;
  std::string api_token_;
  int max_record_seconds_{3};

  State state_{State::IDLE};
  uint8_t audio_buf_[AUDIO_BUF_SIZE];
  volatile size_t buf_pos_{0};  // written from ISR callback
  uint32_t record_start_{0};
  bool should_process_{false};
};

}  // namespace voice_client
}  // namespace esphome
