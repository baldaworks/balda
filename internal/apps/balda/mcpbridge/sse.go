package mcpbridge

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

const maxEndpointBytes = 64 << 10

// Only the legacy transport's first endpoint event is rewritten. Subsequent
// tool messages remain streamed directly, without decoding or buffering them.
func (b *Bridge) rewriteEndpoint(e *endpoint, response *http.Response) error {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return mcpcmd.ErrUnavailable
	}
	if encoding := strings.ToLower(response.Header.Get("Content-Encoding")); encoding != "" && encoding != "identity" {
		if encoding != "gzip" {
			return mcpcmd.ErrUnavailable
		}
		reader, err := gzip.NewReader(response.Body)
		if err != nil {
			return mcpcmd.ErrUnavailable
		}
		response.Body = &gzipBody{Reader: reader, wire: response.Body}
		response.Header.Del("Content-Encoding")
		response.Uncompressed = true
	}
	reader := bufio.NewReader(response.Body)
	remaining := maxEndpointBytes
	var prefix, block strings.Builder
	var event, data string
	for {
		line, err := endpointLine(reader, &remaining)
		if err != nil {
			return mcpcmd.ErrUnavailable
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if data == "" {
				prefix.WriteString(block.String())
				prefix.WriteString(line)
				block.Reset()
				event = ""
				continue
			}
			if event != "endpoint" {
				return mcpcmd.ErrUnavailable
			}
			parsed, err := url.Parse(data)
			if err != nil || parsed.User != nil || parsed.Fragment != "" || strings.ContainsAny(data, "\x00\r\n") {
				return mcpcmd.ErrUnavailable
			}
			target := e.target.ResolveReference(parsed)
			if !strings.EqualFold(target.Scheme, e.target.Scheme) || !strings.EqualFold(target.Host, e.target.Host) {
				return mcpcmd.ErrUnavailable
			}
			path := e.path + "/messages/" + rand.Text()
			b.mu.Lock()
			if b.entries[e.id] != e || b.stopped || len(b.routes) >= maxRoutes {
				b.mu.Unlock()
				return mcpcmd.ErrUnavailable
			}
			b.routes[path] = route{endpoint: e, target: target, message: true}
			b.mu.Unlock()
			prefix.WriteString(block.String())
			prefix.WriteString("data: " + path + "\n\n")
			original := response.Body
			response.Body = &endpointBody{Reader: io.MultiReader(strings.NewReader(prefix.String()), reader), original: original, release: func() { b.mu.Lock(); delete(b.routes, path); b.mu.Unlock() }}
			response.ContentLength = -1
			response.Header.Del("Content-Length")
			response.Header.Del("Content-Md5")
			response.Header.Del("Digest")
			response.Header.Del("Etag")
			return nil
		}
		name, value, hasColon := strings.Cut(trimmed, ":")
		if hasColon {
			value = strings.TrimPrefix(value, " ")
		}
		switch name {
		case "event":
			event = value
			block.WriteString(line)
		case "data":
			if data != "" {
				return mcpcmd.ErrUnavailable
			}
			data = value
		default:
			block.WriteString(line)
		}
	}
}

type gzipBody struct {
	*gzip.Reader
	wire io.ReadCloser
}

func (b *gzipBody) Close() error { return errors.Join(b.Reader.Close(), b.wire.Close()) }

func endpointLine(reader *bufio.Reader, remaining *int) (string, error) {
	var line strings.Builder
	for {
		chunk, err := reader.ReadSlice('\n')
		*remaining -= len(chunk)
		if *remaining < 0 {
			return "", mcpcmd.ErrUnavailable
		}
		line.Write(chunk)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line.String(), err
	}
}

type endpointBody struct {
	io.Reader
	original io.ReadCloser
	release  func()
	once     sync.Once
}

func (b *endpointBody) Close() error {
	var err error
	b.once.Do(func() { err = b.original.Close(); b.release() })
	return err
}
