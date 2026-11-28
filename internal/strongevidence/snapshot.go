// Package strongevidence owns synthetic, local, pinned Git evidence. The
// opaque snapshot cannot be manufactured from a model's echoed digest.
package strongevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

const DeliveryMax = 2 << 20

type File struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	SHA  string `json:"sha256"`
	Body []byte `json:"body"`
}
type Record struct {
	Commit string `json:"commit"`
	Format string `json:"object_format"`
	Files  []File `json:"files"`
}
type Snapshot struct{ data Record }

func Open(ctx context.Context, bare, commit string, paths []string) (*Snapshot, error) {
	files, err := ReadStrongSnapshot(ctx, bare, commit, paths)
	if err != nil {
		return nil, err
	}
	format := "sha1"
	if len(commit) == 64 {
		format = "sha256"
	}
	rec := Record{Commit: commit, Format: format, Files: make([]File, len(files))}
	for i, f := range files {
		rec.Files[i] = File{Path: f.Path, Size: len(f.Body), SHA: f.SHA, Body: f.Body}
	}
	if _, err := rec.Digest(); err != nil {
		return nil, err
	}
	return &Snapshot{data: rec}, nil
}

func (s *Snapshot) Record() Record {
	if s == nil {
		return Record{}
	}
	r := s.data
	r.Files = slices.Clone(r.Files)
	for i := range r.Files {
		r.Files[i].Body = bytes.Clone(r.Files[i].Body)
	}
	return r
}

func (r Record) Digest() (string, error) {
	oidLen := 40
	if r.Format == "sha256" {
		oidLen = 64
	} else if r.Format != "sha1" {
		return "", model.ErrStrongArtifact
	}
	if len(r.Commit) != oidLen || !isOID(r.Commit) || len(r.Files) == 0 || len(r.Files) > 16 {
		return "", model.ErrStrongArtifact
	}
	type entry struct {
		Path string
		Size int
		SHA  string
	}
	manifest := make([]entry, len(r.Files))
	seen := map[string]bool{}
	total := 0
	for i, f := range r.Files {
		total += f.Size
		if f.Path == "" || len(f.Path) > 1024 || strings.ContainsAny(f.Path, "\\:\x00\r\n\t") || seen[f.Path] ||
			f.Size < 0 || total > DeliveryMax || f.Size != len(f.Body) || model.StrongArtifactDigest(f.Body) != f.SHA {
			return "", model.ErrStrongArtifact
		}
		seen[f.Path] = true
		manifest[i] = entry{f.Path, f.Size, f.SHA}
	}
	b, err := json.Marshal(struct {
		Version        int
		Commit, Format string
		Files          []entry
	}{1, r.Commit, r.Format, manifest})
	if err != nil {
		return "", err
	}
	return model.StrongArtifactDigest(b), nil
}
