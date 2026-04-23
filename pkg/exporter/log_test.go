package exporter

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func TestLog_EmitsStructuredRecord(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	log().InfoContext(context.Background(), "test event", "k", "v")

	if !bytes.Contains(buf.Bytes(), []byte(`"component":"exporter"`)) {
		t.Errorf("expected component=exporter, got %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"msg":"test event"`)) {
		t.Errorf("expected msg=test event, got %s", buf.String())
	}
}
