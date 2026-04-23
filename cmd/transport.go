package cmd

import (
	"fmt"
	"strings"

	"github.com/leosocy/diffah/pkg/diff/errs"
)

type ImageRef struct {
	Transport string
	Path      string
}

var supportedInputTransports = map[string]bool{
	"docker-archive": true,
	"oci-archive":    true,
}

var reservedInputTransports = map[string]bool{
	"oci":                true,
	"dir":                true,
	"docker":             true,
	"docker-daemon":      true,
	"containers-storage": true,
	"ostree":             true,
	"sif":                true,
	"tarball":            true,
}

func ParseImageRef(argName, raw string) (ImageRef, error) {
	prefix, rest, ok := splitTransport(raw)
	if !ok {
		return ImageRef{}, newMissingTransportErr(argName, raw)
	}
	if reservedInputTransports[prefix] {
		return ImageRef{}, newReservedTransportErr(argName, prefix)
	}
	if !supportedInputTransports[prefix] {
		return ImageRef{}, newUnsupportedTransportErr(argName, prefix)
	}
	if rest == "" {
		return ImageRef{}, &cliErr{
			cat: errs.CategoryUser,
			msg: fmt.Sprintf("transport %q for %s has empty path: %q", prefix, argName, raw),
		}
	}
	return ImageRef{Transport: prefix, Path: rest}, nil
}

func splitTransport(raw string) (prefix, rest string, ok bool) {
	prefix, rest, found := strings.Cut(raw, ":")
	if !found || prefix == "" {
		return "", "", false
	}
	return prefix, rest, true
}

type cliErr struct {
	cat  errs.Category
	msg  string
	hint string
}

var _ errs.Categorized = (*cliErr)(nil)
var _ errs.Advised = (*cliErr)(nil)

func (e *cliErr) Error() string           { return e.msg }
func (e *cliErr) Category() errs.Category { return e.cat }
func (e *cliErr) NextAction() string      { return e.hint }

func newMissingTransportErr(argName, raw string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "missing transport prefix for %s: %q\n\n", argName, raw)
	sb.WriteString("Image references require a transport prefix. Supported transports:\n")
	sb.WriteString("  docker-archive:PATH     # Docker tar archive (docker save)\n")
	sb.WriteString("  oci-archive:PATH        # OCI tar archive (skopeo copy ... oci-archive:...)\n")
	hint := didYouMean(raw)
	if hint != "" {
		fmt.Fprintf(&sb, "\nDid you mean:  %s\n", hint)
	}
	return &cliErr{cat: errs.CategoryUser, msg: sb.String(), hint: hint}
}

func newReservedTransportErr(argName, prefix string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "transport %q (in %s) is reserved but not yet implemented.\n\n", prefix, argName)
	sb.WriteString("Supported transports in this version:\n")
	sb.WriteString("  docker-archive:PATH     # Docker tar archive (docker save)\n")
	sb.WriteString("  oci-archive:PATH        # OCI tar archive (skopeo copy ... oci-archive:...)\n\n")
	sb.WriteString("Tracking: see CHANGELOG / roadmap for expanded transport support.\n")
	return &cliErr{
		cat:  errs.CategoryUser,
		msg:  sb.String(),
		hint: "see CHANGELOG / roadmap for expanded transport support",
	}
}

func newUnsupportedTransportErr(argName, prefix string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "transport %q for %s is not supported. Supported transports:\n", prefix, argName)
	sb.WriteString("  docker-archive:PATH     # Docker tar archive (docker save)\n")
	sb.WriteString("  oci-archive:PATH        # OCI tar archive (skopeo copy ... oci-archive:...)\n")
	return &cliErr{
		cat: errs.CategoryUser,
		msg: sb.String(),
	}
}

func didYouMean(raw string) string {
	lower := strings.ToLower(raw)
	if strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".tgz") ||
		strings.HasSuffix(lower, ".tar.gz") {
		return "docker-archive:" + raw
	}
	return ""
}
