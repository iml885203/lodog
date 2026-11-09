package casecfg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// LoadDocument reads the legacy document as this level serves it, without the
// parameters the gateway fills in itself (credentials): L1 and up
// apply the case's overlay, L0 serves the document as is.
func LoadDocument(cfg *Config) (*spec.Document, error) {
	var doc *spec.Document
	var err error
	if cfg.Level == L0 {
		doc, err = spec.LoadFile(cfg.OpenAPIPath())
	} else {
		if _, statErr := os.Stat(cfg.OverlayPath()); errors.Is(statErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("level %s needs %s, which does not exist: write it with `lodog enrich --case %s` and review it first, or serve level L0", cfg.Level, cfg.OverlayPath(), cfg.Case)
		}
		doc, err = spec.LoadFileWithOverlay(cfg.OpenAPIPath(), cfg.OverlayPath())
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w (case %q is relative to the directory lodog runs in: run it from the folder that contains the case)", err, cfg.Case)
	}
	if err != nil {
		return nil, err
	}
	if err := HideCredentialParams(doc, cfg.Auth); err != nil {
		return nil, err
	}
	return doc, nil
}

// HideCredentialParams removes from every operation the parameters the gateway
// supplies itself, so an agent never sees, chooses or leaks a credential.
// The gateway injects them at forward time, never from arguments.
func HideCredentialParams(doc *spec.Document, auth AuthConfig) error {
	if err := auth.Validate(); err != nil {
		return err
	}
	owned := auth.Owned()
	for i := range doc.Operations {
		kept := doc.Operations[i].Params[:0:0]
		for _, p := range doc.Operations[i].Params {
			if !gatewayOwns(owned, p) {
				kept = append(kept, p)
			}
		}
		// A clash with a hidden parameter is gone, so its prefix goes too.
		spec.NameArgs(kept)
		doc.Operations[i].Params = kept
	}
	return nil
}

// gatewayOwns reports whether a parameter is one the gateway sets: what the
// auth scheme declares, plus the headers that carry identity whatever the
// scheme (Authorization, Cookie, Proxy-Authorization) and the X-Lodog-*
// headers the recording proxy reads.
func gatewayOwns(owned Owned, p spec.Param) bool {
	name := strings.ToLower(p.Name)
	switch p.In {
	case "header":
		return name == "authorization" || name == "proxy-authorization" || name == "cookie" ||
			strings.HasPrefix(name, "x-lodog-") || slices.ContainsFunc(owned.Headers, func(h string) bool { return strings.ToLower(h) == name })
	case "query":
		return slices.Contains(owned.Queries, p.Name)
	case "cookie":
		return owned.Cookies
	}
	return false
}
