import { useId, type CSSProperties } from 'react';
import { useLocalParticipant, useTrackVolume } from '@livekit/components-react';
import { LocalAudioTrack } from 'livekit-client';

// Analyse the existing published microphone; never request another device stream.
// Keep the 30 Hz meter updates inside this small subtree, away from the video grid.
function MicrophoneFill({ track, clipId }: { track: LocalAudioTrack; clipId: string }) {
  const volume = useTrackVolume(track, {
    fftSize: 64, smoothingTimeConstant: 0.65, minDecibels: -65, maxDecibels: -10,
  });
  const level = Math.min(1, Math.max(0, (volume - 0.025) * 3));
  return <g clipPath={`url(#${clipId})`} className="microphone-fill" data-level={level.toFixed(3)} data-active={level > 0}>
    <g className="microphone-waterline" style={{ '--microphone-waterline': `${16 - level * 15}px` } as CSSProperties}>
      <path className="microphone-wave" d="M0 0 Q4 -1.6 8 0 T16 0 T24 0 T32 0 T40 0 T48 0 V24 H0Z" />
    </g>
  </g>;
}

export function MicrophoneIndicator() {
  const { microphoneTrack, isMicrophoneEnabled } = useLocalParticipant();
  const clipId = `microphone-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`;
  const track = microphoneTrack?.track;
  const active = isMicrophoneEnabled && track instanceof LocalAudioTrack;
  return <svg className="microphone-indicator" viewBox="0 0 24 24" width="24" height="24" aria-hidden="true" focusable="false" data-muted={!isMicrophoneEnabled}>
    <defs><clipPath id={clipId}><rect x="8" y="2" width="8" height="14" rx="4" /></clipPath></defs>
    <rect x="8" y="2" width="8" height="14" rx="4" fill="currentColor" opacity="0.12" />
    {active && <MicrophoneFill track={track} clipId={clipId} />}
    <g fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      <rect x="8" y="2" width="8" height="14" rx="4" />
      <path d="M5 10v2a7 7 0 0 0 14 0v-2M12 19v3M8.5 22h7" />
      {!isMicrophoneEnabled && <path className="microphone-mute-line" d="m3 3 18 18" strokeWidth="2" />}
    </g>
  </svg>;
}
