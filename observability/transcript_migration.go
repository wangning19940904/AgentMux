package observability

import (
	"context"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

type TranscriptPayloadMigrationResult struct {
	Scanned          int64 `json:"scanned"`
	Replaced         int64 `json:"replaced"`
	SourceMissing    int64 `json:"source_missing"`
	ValidationFailed int64 `json:"validation_failed"`
	StoredBytes      int64 `json:"stored_bytes_replaced"`
}

type transcriptPayloadValidation struct {
	replacement   *store.ObservationPayloadSourceReplacement
	storedBytes   int64
	sourceMissing bool
}

// MigrateTranscriptPayloadReferences replaces duplicated encrypted transcript
// bodies with verified JSONL file references. A source is promoted only after
// it materializes to the exact same redacted-content checksum as the existing
// payload; missing or changed files keep their encrypted fallback untouched.
func (r *Runtime) MigrateTranscriptPayloadReferences(ctx context.Context, batchSize int, progress func(TranscriptPayloadMigrationResult)) (TranscriptPayloadMigrationResult, error) {
	var result TranscriptPayloadMigrationResult
	if r == nil || r.Store == nil || r.Recorder == nil || r.Transcript == nil {
		return result, nil
	}
	if batchSize <= 0 || batchSize > 1000 {
		batchSize = 256
	}
	var afterRowID int64
	for {
		candidates, err := r.Store.ListObservationTranscriptPayloadCandidates(ctx, afterRowID, batchSize)
		if err != nil {
			return result, err
		}
		if len(candidates) == 0 {
			break
		}
		afterRowID = candidates[len(candidates)-1].RowID
		result.Scanned += int64(len(candidates))
		validated := make([]transcriptPayloadValidation, len(candidates))
		jobs := make(chan int)
		workers := min(min(runtime.GOMAXPROCS(0), 8), len(candidates))
		var wait sync.WaitGroup
		wait.Add(workers)
		for worker := 0; worker < workers; worker++ {
			go func() {
				defer wait.Done()
				for index := range jobs {
					validated[index] = r.validateTranscriptPayloadReference(ctx, candidates[index])
				}
			}()
		}
		for index := range candidates {
			jobs <- index
		}
		close(jobs)
		wait.Wait()
		if err := ctx.Err(); err != nil {
			return result, err
		}
		replacements := make([]store.ObservationPayloadSourceReplacement, 0, len(candidates))
		var replacementBytes int64
		for _, validation := range validated {
			if validation.replacement != nil {
				replacements = append(replacements, *validation.replacement)
				replacementBytes += validation.storedBytes
			} else if validation.sourceMissing {
				result.SourceMissing++
			} else {
				result.ValidationFailed++
			}
		}
		replaced, err := r.Store.ReplaceObservationPayloadsWithSources(ctx, replacements)
		if err != nil {
			return result, err
		}
		result.Replaced += int64(replaced)
		if replaced == len(replacements) {
			result.StoredBytes += replacementBytes
		}
		if progress != nil {
			progress(result)
		}
	}
	return result, nil
}

func (r *Runtime) validateTranscriptPayloadReference(ctx context.Context, candidate store.ObservationTranscriptPayloadCandidate) transcriptPayloadValidation {
	source, err := r.Transcript.BuildPayloadSource(ctx, candidate.Envelope)
	if err != nil {
		return transcriptPayloadValidation{sourceMissing: os.IsNotExist(err)}
	}
	ref := core.ObservationPayloadRef{
		ID: candidate.PayloadID, Storage: source.Storage, ContentType: candidate.ContentType,
		OriginalBytes: candidate.OriginalBytes, StoredBytes: 0, Redacted: candidate.Redacted,
		ExpiresAt: candidate.ExpiresAt, SourcePath: source.Path, SourceOffset: source.Offset,
		SourceLength: source.Length, SourceIdentity: source.Identity, SourceSHA256: source.SHA256,
		SourceRuntime: source.Runtime, SourceClass: source.Class, ContentSHA256: candidate.ContentSHA256,
	}
	probe := candidate.Envelope
	probeRef := ref
	// Expired encrypted copies are still checksum-validated during the
	// migration; the persisted reference keeps its original expiry and remains
	// unreadable through the normal API afterward.
	probeRef.ExpiresAt = time.Time{}
	probe.PayloadRef = &probeRef
	content, _, err := r.Recorder.ReadEnvelopePayload(ctx, probe)
	if err != nil {
		return transcriptPayloadValidation{}
	}
	for index := range content {
		content[index] = 0
	}
	return transcriptPayloadValidation{
		replacement: &store.ObservationPayloadSourceReplacement{
			RowID: candidate.RowID, EventID: candidate.Envelope.EventID, PayloadID: candidate.PayloadID,
			Envelope: candidate.Envelope, Ref: ref,
		},
		storedBytes: candidate.StoredBytes,
	}
}
