import { ParticipantTile, VideoTrack, ParticipantName, TrackMutedIndicator, ConnectionQualityIndicator, useEnsureTrackRef, type ParticipantTileProps } from '@livekit/components-react';
import { Track } from 'livekit-client';
import { tr, useLocale } from './i18n';
export function LocalizedTile(props: ParticipantTileProps) {
  useLocale();
  const track = useEnsureTrackRef(props.trackRef);
  const initials = (track.participant.name || track.participant.identity).trim().split(/\s+/).slice(0, 2).map(word => Array.from(word)[0] || '').join('').toLocaleUpperCase();
  const screen = track.source === Track.Source.ScreenShare;
  return <ParticipantTile {...props}>
    {track.publication && <VideoTrack trackRef={track as Parameters<typeof VideoTrack>[0]['trackRef']} />}
    <div className="lk-participant-placeholder" aria-label={tr('Camera is off')}><span className="tile-avatar" aria-hidden="true">{initials || '·'}</span></div>
    <div className="lk-participant-metadata"><div className="lk-participant-metadata-item">
      {!screen && <TrackMutedIndicator trackRef={{participant:track.participant, source:Track.Source.Microphone}} show="muted" aria-label={tr('Microphone')} />}
      <ParticipantName />{screen && <span> · {tr('Screen share')}</span>}
    </div><ConnectionQualityIndicator participant={track.participant} aria-label={tr('Connection quality')} /></div>
  </ParticipantTile>;
}
