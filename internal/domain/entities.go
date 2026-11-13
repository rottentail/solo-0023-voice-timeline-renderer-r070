package domain

import "time"

const CurrentRenderPersistenceVersion = 2

const (
	RenderRecordJob     = "render_job"
	RenderRecordReceipt = "render_receipt"
)

type RenderPersistence struct {
	Version    int       `json:"version"`
	RecordKind string    `json:"record_kind"`
	WrittenAt  time.Time `json:"written_at"`
}

func NewRenderPersistence(recordKind string, now time.Time) RenderPersistence {
	return RenderPersistence{
		Version:    CurrentRenderPersistenceVersion,
		RecordKind: recordKind,
		WrittenAt:  now.UTC(),
	}
}

func (metadata RenderPersistence) Validate(recordKind string) error {
	if metadata.Version != CurrentRenderPersistenceVersion {
		return E(
			KindInternal,
			"unsupported_render_record",
			"",
			"render record was written by an unsupported persistence version",
		)
	}
	if metadata.RecordKind != recordKind {
		return E(
			KindInternal,
			"render_record_kind_mismatch",
			"",
			"render record kind does not match its storage location",
		)
	}
	if metadata.WrittenAt.IsZero() {
		return E(
			KindInternal,
			"render_record_time_missing",
			"",
			"render record is missing its persistence timestamp",
		)
	}
	return nil
}

// IsLegacy reports whether the record predates the current persistence format.
// Records written before the persistence metadata was introduced decode to an
// all-zero value; an older explicit version is equally recognizable. Anything
// that is not strictly older than the current version is never treated as a
// legacy record.
func (metadata RenderPersistence) IsLegacy() bool {
	return metadata.Version >= 0 && metadata.Version < CurrentRenderPersistenceVersion
}

// ValidateLegacy checks the persistence metadata of a record that predates the
// current format. Historical records may omit some or all of this metadata, so
// only internally contradictory values are rejected here. The caller must
// additionally prove from the record body and the stored files that the record
// really belongs to the requested render before it may be used.
func (metadata RenderPersistence) ValidateLegacy(recordKind string) error {
	if !metadata.IsLegacy() {
		return E(
			KindInternal,
			"unsupported_render_record",
			"",
			"render record was written by an unsupported persistence version",
		)
	}
	if metadata.RecordKind != "" && metadata.RecordKind != recordKind {
		return E(
			KindInternal,
			"render_record_kind_mismatch",
			"",
			"render record kind does not match its storage location",
		)
	}
	return nil
}

func (job RenderJob) ValidatePersistence() error {
	return job.Persistence.Validate(RenderRecordJob)
}

func (receipt RenderReceipt) ValidatePersistence() error {
	return receipt.Persistence.Validate(RenderRecordReceipt)
}

type SourceAsset struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Digest        string    `json:"digest"`
	ByteCount     int64     `json:"byte_count"`
	SampleRate    int       `json:"sample_rate"`
	Channels      int       `json:"channels"`
	BitsPerSample int       `json:"bits_per_sample"`
	FrameCount    int64     `json:"frame_count"`
	DurationMS    int64     `json:"duration_ms"`
	PeakDBFS      float64   `json:"peak_dbfs"`
	RMSDBFS       float64   `json:"rms_dbfs"`
	CreatedAt     time.Time `json:"created_at"`
}

type TimelineSpan struct {
	SourceID         string  `json:"source_id"`
	SourceStartFrame int64   `json:"source_start_frame"`
	SourceEndFrame   int64   `json:"source_end_frame"`
	GainDB           float64 `json:"gain_db"`
	CrossfadeFrames  int64   `json:"crossfade_frames"`
}

type Timeline struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	SampleRate     int            `json:"sample_rate"`
	OutputChannels int            `json:"output_channels"`
	TargetPeakDBFS float64        `json:"target_peak_dbfs"`
	Spans          []TimelineSpan `json:"spans"`
	State          string         `json:"state"`
	OutputFrames   int64          `json:"output_frames"`
	CreatedAt      time.Time      `json:"created_at"`
}

type RenderState string

const (
	RenderQueued     RenderState = "queued"
	RenderRendering  RenderState = "rendering"
	RenderFinalizing RenderState = "finalizing"
	RenderCompleted  RenderState = "completed"
	RenderFailed     RenderState = "failed"
)

type OutputAsset struct {
	Digest        string  `json:"digest"`
	ByteCount     int64   `json:"byte_count"`
	SampleRate    int     `json:"sample_rate"`
	Channels      int     `json:"channels"`
	BitsPerSample int     `json:"bits_per_sample"`
	FrameCount    int64   `json:"frame_count"`
	DurationMS    int64   `json:"duration_ms"`
	PeakDBFS      float64 `json:"peak_dbfs"`
	RMSDBFS       float64 `json:"rms_dbfs"`
}

type RenderJob struct {
	ID            string            `json:"id"`
	TimelineID    string            `json:"timeline_id"`
	State         RenderState       `json:"state"`
	Error         *ErrorDetail      `json:"error,omitempty"`
	Output        *OutputAsset      `json:"output,omitempty"`
	ReceiptDigest string            `json:"receipt_digest,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Persistence   RenderPersistence `json:"persistence"`
}

type ReceiptSpan struct {
	Index            int     `json:"index"`
	SourceID         string  `json:"source_id"`
	SourceDigest     string  `json:"source_digest"`
	SourceStartFrame int64   `json:"source_start_frame"`
	SourceEndFrame   int64   `json:"source_end_frame"`
	OutputStartFrame int64   `json:"output_start_frame"`
	OutputEndFrame   int64   `json:"output_end_frame"`
	OutputFrames     int64   `json:"output_frames"`
	CrossfadeFrames  int64   `json:"crossfade_frames"`
	GainDB           float64 `json:"gain_db"`
	ChannelAction    string  `json:"channel_action"`
}

type NormalizationReceipt struct {
	RequestedPeakDBFS float64 `json:"requested_peak_dbfs"`
	BeforePeakDBFS    float64 `json:"before_peak_dbfs"`
	AppliedGainDB     float64 `json:"applied_gain_db"`
	OutputPeakDBFS    float64 `json:"output_peak_dbfs"`
}

type RenderReceipt struct {
	SchemaVersion int                  `json:"schema_version"`
	RenderID      string               `json:"render_id"`
	TimelineID    string               `json:"timeline_id"`
	Output        OutputAsset          `json:"output"`
	Normalization NormalizationReceipt `json:"normalization"`
	Spans         []ReceiptSpan        `json:"spans"`
	Warnings      []string             `json:"warnings"`
	CreatedAt     time.Time            `json:"created_at"`
	Persistence   RenderPersistence    `json:"persistence"`
}

func ValidRenderTransition(from, to RenderState) bool {
	switch from {
	case RenderQueued:
		return to == RenderRendering || to == RenderFailed
	case RenderRendering:
		return to == RenderFinalizing || to == RenderFailed
	case RenderFinalizing:
		return to == RenderCompleted || to == RenderFailed
	default:
		return false
	}
}

func (job *RenderJob) Transition(to RenderState, detail *ErrorDetail, now time.Time) error {
	if job.State == to {
		return nil
	}
	if !ValidRenderTransition(job.State, to) {
		return E(
			KindConflict,
			"invalid_state_transition",
			"state",
			"render job cannot move from "+string(job.State)+" to "+string(to),
		)
	}
	if to == RenderFailed && detail == nil {
		return E(KindInternal, "missing_failure_detail", "error", "a failed render requires an error detail")
	}
	if to != RenderFailed {
		detail = nil
	}
	job.State = to
	job.Error = detail
	job.UpdatedAt = now.UTC()
	return nil
}
