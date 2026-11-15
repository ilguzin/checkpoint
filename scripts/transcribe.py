#!/usr/bin/env python3
"""Isolated local ASR: use a preinstalled model only, without network access."""
import argparse
import json
import sys
import wave


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--model', required=True)
    parser.add_argument('--language', choices=('auto', 'ru', 'en'), default='auto')
    parser.add_argument('--threads', type=int, default=2)
    parser.add_argument('audio')
    args = parser.parse_args()
    import numpy as np
    from faster_whisper import WhisperModel
    from faster_whisper.vad import get_speech_timestamps, VadOptions
    model = WhisperModel(args.model, device='cpu', compute_type='int8',
                         cpu_threads=args.threads, num_workers=1,
                         local_files_only=True)
    output = []
    with wave.open(args.audio, 'rb') as audio:
        if (audio.getnchannels(), audio.getsampwidth(), audio.getframerate()) != (1, 2, 16000):
            raise ValueError('ASR requires mono 16 kHz PCM16 WAV')
        offset = 0
        while True:
            raw = audio.readframes(30 * 16000)
            if not raw:
                break
            samples = np.frombuffer(raw, dtype='<i2').astype(np.float32) / 32768.0
            # Re-detect per speech span, bounded to 15s. Auto is scoped to the
            # two supported languages; unrelated language guesses are not used
            # to silently translate or discard a Russian/English meeting.
            spans = get_speech_timestamps(samples, VadOptions(
                min_silence_duration_ms=500, max_speech_duration_s=15,
                speech_pad_ms=200))
            for span in spans:
                clip = samples[span['start']:span['end']]
                language = args.language
                probability = 1.0
                if language == 'auto':
                    _, _, probabilities = model.detect_language(audio=clip)
                    language, probability = max(
                        ((code, probability) for code, probability in probabilities if code in ('ru', 'en')),
                        key=lambda item: item[1])
                segments, info = model.transcribe(
                    clip, language=language, task='transcribe', beam_size=5,
                    vad_filter=False, condition_on_previous_text=False,
                    hallucination_silence_threshold=2)
                for segment in segments:
                    text = segment.text.strip()
                    if text and segment.avg_logprob > -1.0:
                        start = offset + round(span['start'] * 1000 / 16000)
                        output.append({'startMs': start + round(segment.start * 1000),
                                       'endMs': start + round(segment.end * 1000),
                                       'text': text, 'language': language,
                                       'languageProbability': probability})
            offset += round(len(samples) * 1000 / 16000)
    json.dump(output, sys.stdout, ensure_ascii=False)


if __name__ == '__main__':
    main()
