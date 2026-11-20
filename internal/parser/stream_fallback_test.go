package parser

import (
	"strings"
	"testing"

	"github.com/coregx/gxpdf/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamWithIndirectLengthPDF builds a one-page PDF whose page content stream
// takes its /Length from another object. The parser cannot size such a stream
// and falls back to scanning for endstream, so the bytes around the data are
// exactly what these tests probe.
func streamWithIndirectLengthPDF(body string) []byte {
	return testutil.BuildPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>",
		body,
		"1",
	)
}

func pageContentStream(t *testing.T, r *Reader) *Stream {
	t.Helper()
	page, err := r.GetPage(0)
	require.NoError(t, err)
	reference, ok := page.Get("Contents").(*IndirectReference)
	require.True(t, ok, "Contents is %T", page.Get("Contents"))
	object, err := r.GetObject(reference.Number)
	require.NoError(t, err)
	stream, ok := object.(*Stream)
	require.True(t, ok, "content object is %T", object)
	return stream
}

// TestStreamWithoutDirectLengthKeepsExactContent covers the end-of-line markers
// around stream data when /Length is missing or indirect: both markers are
// syntax and must not reach the data, and no real byte may be trimmed either.
func TestStreamWithoutDirectLengthKeepsExactContent(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "CRLF after the stream keyword is not data",
			body: "<< /Length 5 0 R >>\nstream\r\nPAYLOAD\nendstream",
			want: "PAYLOAD\n",
		},
		{
			name: "LF after the stream keyword is not data",
			body: "<< /Length 5 0 R >>\nstream\nPAYLOAD\nendstream",
			want: "PAYLOAD\n",
		},
		{
			name: "no end-of-line before endstream",
			body: "<< /Length 5 0 R >>\nstream\r\nPAYLOADendstream",
			want: "PAYLOAD",
		},
		{
			// Without /Length the marker before endstream cannot be told from
			// a last byte of 0x0A that belongs to the data, so it is kept:
			// trimming it corrupted the flate streams of golang.pdf,
			// gobook.0.pdf and us-031a.pdf.
			name: "end-of-line before endstream is kept",
			body: "<< /Length 5 0 R >>\nstream\r\nPAYLOAD\nendstream",
			want: "PAYLOAD\n",
		},
		{
			name: "CRLF before endstream is kept",
			body: "<< /Length 5 0 R >>\nstream\r\nPAYLOAD\r\nendstream",
			want: "PAYLOAD\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader, err := OpenPDFFromBytes(streamWithIndirectLengthPDF(tt.body))
			require.NoError(t, err)
			t.Cleanup(func() { _ = reader.Close() })

			assert.Equal(t, tt.want, string(pageContentStream(t, reader).Content()))
		})
	}
}

// TestStreamWithoutDirectLengthAndWithoutEndstreamFails checks that a truncated
// stream ends in an error rather than a hang or a silent empty stream.
func TestStreamWithoutDirectLengthAndWithoutEndstreamFails(t *testing.T) {
	reader, err := OpenPDFFromBytes(streamWithIndirectLengthPDF("<< /Length 5 0 R >>\nstream\r\nPAYLOAD"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })

	page, err := reader.GetPage(0)
	require.NoError(t, err)
	reference, ok := page.Get("Contents").(*IndirectReference)
	require.True(t, ok)

	_, err = reader.GetObject(reference.Number)
	require.Error(t, err)
	assert.ErrorContains(t, err, "unexpected EOF")
}

// TestStreamWithoutDirectLengthLongerThanTheReadBuffer covers data that spans
// several reads. The sizes sweep the endstream keyword across the end of a
// read, where it arrives in two parts, and the patterns hold starts of the
// keyword that are data and must not end the stream.
func TestStreamWithoutDirectLengthLongerThanTheReadBuffer(t *testing.T) {
	const bufferSize = 4096 // bufio's default, which the lexer reads with

	tests := []struct {
		name    string
		pattern string
		from    int
		to      int
	}{
		{name: "keyword across the first read", pattern: "x", from: bufferSize - 100, to: bufferSize + 20},
		{name: "keyword across a later read", pattern: "x", from: 3*bufferSize - 100, to: 3*bufferSize + 20},
		{name: "data that starts like the keyword", pattern: "endstrea", from: bufferSize - 100, to: bufferSize + 20},
		{name: "data that ends like the keyword", pattern: "ndstream", from: 2*bufferSize - 100, to: 2*bufferSize + 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for size := tt.from; size <= tt.to; size++ {
				payload := strings.Repeat(tt.pattern, size/len(tt.pattern)+1)[:size]
				body := "<< /Length 5 0 R >>\nstream\r\n" + payload + "\nendstream"

				reader, err := OpenPDFFromBytes(streamWithIndirectLengthPDF(body))
				require.NoError(t, err, "size %d", size)
				content := string(pageContentStream(t, reader).Content())
				require.NoError(t, reader.Close())

				require.Equal(t, payload+"\n", content, "size %d", size)
			}
		})
	}
}
