package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"glowworm/internal/core/imports"
	"glowworm/internal/importer"
)

func (a *App) apiImportPreviewDate(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Sample string `json:"sample"`
		Format string `json:"format"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	in.Sample = strings.TrimSpace(in.Sample)
	in.Format = strings.TrimSpace(in.Format)
	out := map[string]any{"iso": "", "layout": "", "sample": in.Sample}
	if in.Sample == "" {
		writeJSON(w, out)
		return nil
	}
	if in.Format != "" {
		if t, err := time.Parse(in.Format, in.Sample); err == nil {
			out["iso"] = t.Format("2006-01-02")
			out["layout"] = in.Format
			writeJSON(w, out)
			return nil
		}
	}
	iso, layout := importer.TryDate(in.Sample)
	out["iso"] = iso
	out["layout"] = layout
	writeJSON(w, out)
	return nil
}

func (a *App) apiImportAIDetect(w http.ResponseWriter, r *http.Request) error {
	batchID := r.PathValue("id")
	batch, err := a.Imports.Get(batchID)
	if err != nil {
		return err
	}
	data, err := a.importFileBytes(batchID)
	if err != nil {
		return err
	}
	b, err := a.Imports.AIDetect(batchID, data, batch.Filename)
	if err != nil {
		if serviceError(w, err) {
			return nil
		}
		return err
	}
	writeJSON(w, b)
	return nil
}

func (a *App) apiImportCategoriseStream(w http.ResponseWriter, r *http.Request) {
	a.streamCategorise(w, r, a.Imports.Categorise)
}

func (a *App) apiImportCategoriseAIStream(w http.ResponseWriter, r *http.Request) {
	a.streamCategorise(w, r, a.Imports.CategoriseAI)
}

func (a *App) streamCategorise(w http.ResponseWriter, r *http.Request, run func(context.Context, string, func(imports.Progress)) error) {
	batchID := r.PathValue("id")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	rc.Flush()

	ctx := r.Context()
	cancelled := false
	emit := func(p imports.Progress) {
		if cancelled {
			return
		}
		select {
		case <-ctx.Done():
			cancelled = true
			return
		default:
		}
		buf, err := json.Marshal(p)
		if err != nil {
			return
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			cancelled = true
			return
		}
		if _, err := w.Write(buf); err != nil {
			cancelled = true
			return
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			cancelled = true
			return
		}
		_ = rc.Flush()
	}
	if err := run(ctx, batchID, emit); err != nil {
		if ctx.Err() != nil {
			return
		}
		var ie *imports.ValidationError
		msg := err.Error()
		if errors.As(err, &ie) {
			msg = ie.Message
		}
		emit(imports.Progress{Phase: "error", Message: msg})
	}
}
