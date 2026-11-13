package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
)

func (repo *Repository) SaveJob(job domain.RenderJob) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	path := repo.path("jobs", job.ID+".json")
	if err := writeJSONAtomic(path, job, 0o640); err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot write render job", err)
	}
	return nil
}

func (repo *Repository) GetJob(id string) (domain.RenderJob, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	job, err := repo.readJob(id)
	if err != nil {
		return domain.RenderJob{}, err
	}
	persistenceErr := job.ValidatePersistence()
	if persistenceErr == nil {
		return job, nil
	}
	if !job.Persistence.IsLegacy() {
		return domain.RenderJob{}, domain.Wrap(
			domain.KindInternal,
			"storage_error",
			"",
			"cannot read render job persistence metadata",
			persistenceErr,
		)
	}
	if err := job.Persistence.ValidateLegacy(domain.RenderRecordJob); err != nil {
		return domain.RenderJob{}, domain.Wrap(
			domain.KindInternal,
			"storage_error",
			"",
			"cannot read render job persistence metadata",
			err,
		)
	}
	return job, repo.validateLegacyJob(id, job)
}

func (repo *Repository) CommitRenderArtifacts(id string, wav []byte, receipt domain.RenderReceipt) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	final := repo.path("renders", id)
	if _, err := os.Stat(final); err == nil {
		return domain.E(domain.KindConflict, "render_conflict", "render_id", "render artifacts already exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot inspect render destination", err)
	}
	temp, err := os.MkdirTemp(repo.path("tmp"), "render-*")
	if err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot create render staging directory", err)
	}
	defer func() {
		_ = os.RemoveAll(temp)
	}()
	if err := writeBytes(filepath.Join(temp, "output.wav"), wav, 0o640); err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot write render output", err)
	}
	if err := writeJSONAtomic(filepath.Join(temp, "receipt.json"), receipt, 0o640); err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot write render receipt", err)
	}
	if err := os.Rename(temp, final); err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot commit render artifacts", err)
	}
	if err := syncDirectory(repo.path("renders")); err != nil {
		return domain.Wrap(domain.KindInternal, "storage_error", "", "cannot sync render index", err)
	}
	return nil
}

func (repo *Repository) GetRenderOutput(id string) ([]byte, domain.RenderReceipt, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	payload, receipt, err := repo.readRenderArtifacts(id)
	if err != nil {
		return nil, domain.RenderReceipt{}, err
	}
	persistenceErr := receipt.ValidatePersistence()
	if persistenceErr == nil {
		return payload, receipt, nil
	}
	if !receipt.Persistence.IsLegacy() {
		return nil, domain.RenderReceipt{}, domain.Wrap(
			domain.KindInternal,
			"storage_error",
			"",
			"cannot read render receipt persistence metadata",
			persistenceErr,
		)
	}
	if err := receipt.Persistence.ValidateLegacy(domain.RenderRecordReceipt); err != nil {
		return nil, domain.RenderReceipt{}, domain.Wrap(
			domain.KindInternal,
			"storage_error",
			"",
			"cannot read render receipt persistence metadata",
			err,
		)
	}
	if err := validateLegacyRenderArtifacts(id, payload, receipt); err != nil {
		return nil, domain.RenderReceipt{}, err
	}
	return payload, receipt, nil
}

// readJob loads a job record. The caller must hold at least the read lock.
func (repo *Repository) readJob(id string) (domain.RenderJob, error) {
	var job domain.RenderJob
	if err := readJSON(repo.path("jobs", id+".json"), &job); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.RenderJob{}, domain.E(domain.KindNotFound, "render_not_found", "render_id", "render job does not exist")
		}
		return domain.RenderJob{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read render job", err)
	}
	return job, nil
}

// readRenderArtifacts loads the published receipt and output bytes without
// interpreting their persistence metadata; callers validate that separately.
// The caller must hold at least the read lock.
func (repo *Repository) readRenderArtifacts(id string) ([]byte, domain.RenderReceipt, error) {
	var receipt domain.RenderReceipt
	if err := readJSON(repo.path("renders", id, "receipt.json"), &receipt); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.RenderReceipt{}, domain.E(domain.KindUnready, "render_not_completed", "render_id", "render output is not available")
		}
		return nil, domain.RenderReceipt{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read render receipt", err)
	}
	payload, err := os.ReadFile(repo.path("renders", id, "output.wav"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.RenderReceipt{}, domain.E(domain.KindUnready, "render_not_completed", "render_id", "render output is not available")
		}
		return nil, domain.RenderReceipt{}, domain.Wrap(domain.KindInternal, "storage_error", "", "cannot read render output", err)
	}
	return payload, receipt, nil
}

// validateLegacyJob proves that a job record written before the current
// persistence format really describes the requested render. Historical records
// are accepted only when their identity matches and, for a completed render,
// when the published artifacts are present, complete, and mutually consistent.
// It never rewrites the stored files, so a legacy record keeps its original
// state, bytes, and digest across restarts.
func (repo *Repository) validateLegacyJob(id string, job domain.RenderJob) error {
	if job.ID != id {
		return legacyStorageError("legacy render job does not belong to the requested render identifier", nil)
	}
	if err := domain.ValidateResourceID("timeline_id", job.TimelineID); err != nil {
		return legacyStorageError("legacy render job is missing a valid timeline reference", err)
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() {
		return legacyStorageError("legacy render job is missing its timestamps", nil)
	}
	if job.Output != nil {
		if err := validateOutputAsset(job.Output); err != nil {
			return legacyStorageError("legacy render job has incomplete output metadata", err)
		}
	}
	switch job.State {
	case domain.RenderCompleted:
		if job.Output == nil {
			return legacyStorageError("legacy completed render job is missing its output metadata", nil)
		}
		payload, receipt, err := repo.readRenderArtifacts(id)
		if err != nil {
			return legacyStorageError("legacy completed render job has no readable published artifacts", err)
		}
		if err := validateLegacyReceiptPersistence(receipt); err != nil {
			return err
		}
		if err := validateLegacyRenderArtifacts(id, payload, receipt); err != nil {
			return err
		}
		if job.Output.Digest != receipt.Output.Digest {
			return legacyStorageError("legacy render job output digest disagrees with the published receipt", nil)
		}
		if job.TimelineID != receipt.TimelineID {
			return legacyStorageError("legacy render job timeline disagrees with the published receipt", nil)
		}
		if job.ReceiptDigest != "" && job.ReceiptDigest != receipt.Output.Digest {
			return legacyStorageError("legacy render job receipt digest disagrees with the published receipt", nil)
		}
		if !sameOutputMeasurements(job.Output, &receipt.Output) {
			return legacyStorageError("legacy render job output metadata disagrees with the published receipt", nil)
		}
	case domain.RenderFailed:
		if job.Error == nil || job.Error.Code == "" || job.Error.Message == "" {
			return legacyStorageError("legacy failed render job is missing its stable error detail", nil)
		}
		if job.Output != nil || job.ReceiptDigest != "" {
			return legacyStorageError("legacy failed render job unexpectedly exposes render artifacts", nil)
		}
	case domain.RenderQueued, domain.RenderRendering, domain.RenderFinalizing:
		// An active historical job is returned exactly as stored; it is neither
		// resumed nor rewritten. Only completed renders resolve artifacts.
		if job.Output != nil || job.ReceiptDigest != "" {
			return legacyStorageError("legacy active render job unexpectedly carries output metadata", nil)
		}
	default:
		return legacyStorageError("legacy render job carries an unknown state", nil)
	}
	return nil
}

func validateLegacyReceiptPersistence(receipt domain.RenderReceipt) error {
	if err := receipt.ValidatePersistence(); err == nil {
		return nil
	} else if !receipt.Persistence.IsLegacy() {
		return legacyStorageError("published render receipt persistence metadata is unsupported", err)
	}
	if err := receipt.Persistence.ValidateLegacy(domain.RenderRecordReceipt); err != nil {
		return legacyStorageError("published render receipt persistence metadata is inconsistent", err)
	}
	return nil
}

// validateLegacyRenderArtifacts proves that the published output and receipt
// belong to the requested render and describe the exact stored bytes.
func validateLegacyRenderArtifacts(id string, payload []byte, receipt domain.RenderReceipt) error {
	if receipt.RenderID != id {
		return legacyStorageError("published receipt does not belong to the requested render identifier", nil)
	}
	if err := domain.ValidateResourceID("timeline_id", receipt.TimelineID); err != nil {
		return legacyStorageError("published receipt is missing a valid timeline reference", err)
	}
	if receipt.SchemaVersion < 0 || receipt.SchemaVersion > 1 {
		return legacyStorageError("published receipt has an unsupported schema version", nil)
	}
	if len(receipt.Spans) < 1 {
		return legacyStorageError("published receipt is missing its span records", nil)
	}
	if err := validateOutputAsset(&receipt.Output); err != nil {
		return legacyStorageError("published receipt has incomplete output metadata", err)
	}

	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	if receipt.Output.Digest != digestText {
		return legacyStorageError("published output digest does not match the stored media bytes", nil)
	}
	if receipt.Output.ByteCount != int64(len(payload)) {
		return legacyStorageError("published output byte count does not match the stored media bytes", nil)
	}

	_, info, err := audiocodec.DecodeWAV(payload)
	if err != nil {
		return legacyStorageError("published output media cannot be decoded", err)
	}
	if info.SampleRate != receipt.Output.SampleRate ||
		info.Channels != receipt.Output.Channels ||
		info.BitsPerSample != receipt.Output.BitsPerSample ||
		info.FrameCount != receipt.Output.FrameCount ||
		framesToMilliseconds(info.FrameCount, info.SampleRate) != receipt.Output.DurationMS {
		return legacyStorageError("published output media disagrees with the receipt metadata", nil)
	}
	return nil
}

func sameOutputMeasurements(left, right *domain.OutputAsset) bool {
	return left.ByteCount == right.ByteCount &&
		left.SampleRate == right.SampleRate &&
		left.Channels == right.Channels &&
		left.BitsPerSample == right.BitsPerSample &&
		left.FrameCount == right.FrameCount &&
		left.DurationMS == right.DurationMS
}

func validateOutputAsset(asset *domain.OutputAsset) error {
	if asset == nil {
		return errors.New("output metadata is absent")
	}
	if len(asset.Digest) != 64 {
		return errors.New("output digest is not a SHA-256 value")
	}
	if _, err := hex.DecodeString(asset.Digest); err != nil {
		return errors.New("output digest is not a hexadecimal value")
	}
	if asset.ByteCount < 1 || asset.FrameCount < 1 || asset.DurationMS < 1 {
		return errors.New("output measurements are incomplete")
	}
	if asset.SampleRate < 8000 || asset.SampleRate > 192000 {
		return errors.New("output sample rate is outside the accepted range")
	}
	if asset.Channels != 1 && asset.Channels != 2 {
		return errors.New("output channel count is not mono or stereo")
	}
	if asset.BitsPerSample != 16 {
		return errors.New("output sample format is not 16-bit integer PCM")
	}
	return nil
}

func framesToMilliseconds(frames int64, sampleRate int) int64 {
	if frames <= 0 || sampleRate <= 0 {
		return 0
	}
	return int64(float64(frames) * 1000 / float64(sampleRate))
}

func legacyStorageError(message string, cause error) error {
	if cause == nil {
		return domain.E(domain.KindInternal, "storage_error", "", message)
	}
	return domain.Wrap(domain.KindInternal, "storage_error", "", message, cause)
}
