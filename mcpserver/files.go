package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/aristath/songya"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const resourceFileLimit = 32 << 20
const resourcePageSize = 1 << 20

type attachmentRef struct {
	songya.Attachment
	URI      string `json:"uri"`
	PagesURI string `json:"pages_uri,omitempty"`
}

func attachmentURI(id, filename string) string {
	return (&url.URL{Scheme: "memory", Host: "files", Path: "/" + id + "/" + filename}).String()
}
func newAttachmentRef(id string, file songya.Attachment) attachmentRef {
	ref := attachmentRef{Attachment: file, URI: attachmentURI(id, file.Filename)}
	if file.Bytes > resourceFileLimit {
		ref.PagesURI = ref.URI + "/pages/0"
	}
	return ref
}
func attachmentLink(file attachmentRef) *mcp.ResourceLink {
	return &mcp.ResourceLink{URI: file.URI, Name: file.Filename, MIMEType: file.MediaType, Size: &file.Bytes}
}

type readFileInput struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Offset   int64  `json:"offset"`
}

func (a *App) registerFiles(server *mcp.Server) {
	tool(server, "list_files", "List this record's saved attachments, with filenames, formats and original MCP resource URIs. Call read_file with id and filename to read text or see a photo. revision is needed for file changes. Follow a positive next as offset with the same id to list more.", schema(map[string]any{"id": str("Record ID"), "offset": count(), "limit": count()}, "id"), true, func(ctx context.Context, input struct {
		ID     string `json:"id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}) (any, error) { listing, err := a.Store.Attachments(ctx, input.ID); if err != nil {
		return nil, err
	}; start := min(max(0, input.Offset), len(listing.Files)); end := min(start+limit(input.Limit, 16, 16), len(listing.Files)); refs := []attachmentRef{}; for _, file := range listing.Files[start:end] {
		candidate := append(refs, newAttachmentRef(input.ID, file))
		data, _ := json.Marshal(candidate)
		if len(data) > 10000 && len(refs) > 0 {
			break
		}
		refs = candidate
	}; end = start + len(refs); next := 0; if end < len(listing.Files) {
		next = end
	}; out := result(map[string]any{"files": refs, "revision": listing.Revision, "pending": listing.Pending, "diagnostics": listing.Diagnostics, "offset": start, "next": next, "truncated": next > 0}, nil); for _, ref := range refs {
		out.Content = append(out.Content, attachmentLink(ref))
	}; return out, nil })
	tool(server, "read_file", "Read one saved attachment using its record id and exact filename from list_files. Photos return actual image content for a vision-capable model, including HEIC/HEIF. Text returns at most 16384 bytes; follow next as offset with the same id and filename. Other formats return an original MCP resource reference, not interpreted contents. Never claim to see or hear content that was not returned.", schema(map[string]any{"id": str("Record ID"), "filename": str("Actual filename from list_files"), "offset": count()}, "id", "filename"), true, a.readAttachment)
	server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "memory://files/{id}/{filename}", Name: "Original saved attachment", Description: "Read original attachment bytes. Large files use the pages_uri returned by list_files."}, a.attachmentResource)
	server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "memory://files/{id}/{filename}/pages/{offset}", Name: "Original attachment byte page", Description: "Read up to 1 MiB of an original file. Returned metadata gives the next URI and original format; clients can assemble the bytes."}, a.attachmentResource)
}

func (a *App) readAttachment(ctx context.Context, input readFileInput) (any, error) {
	file, info, err := a.Store.OpenAttachment(ctx, input.ID, input.Filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	ref := newAttachmentRef(input.ID, info)
	metadata := map[string]any{"id": input.ID, "file": ref}
	var out *mcp.CallToolResult
	switch info.Preview {
	case "image":
		data, mediaType, err := songya.AttachmentImage(ctx, file, 1600)
		metadata["view"] = "image"
		if info.MediaType == "image/gif" {
			metadata["note"] = "This preview shows the first frame, not the whole animation."
		}
		out = result(metadata, err)
		if err == nil {
			out.Content = append(out.Content, &mcp.ImageContent{Data: data, MIMEType: mediaType})
		}
	case "text":
		offset := max(0, input.Offset)
		text, next, err := a.Store.ReadAttachmentText(ctx, input.ID, input.Filename, offset, 16384)
		metadata["text"], metadata["offset"], metadata["next"], metadata["truncated"] = text, offset, next, next > 0
		out = result(metadata, err)
	default:
		metadata["view"] = "resource"
		metadata["note"] = "Original file available through this MCP resource. No image, transcript or extracted document text was returned. Use a client that supports this format to inspect it."
		out = result(metadata, nil)
	}
	out.Content = append(out.Content, attachmentLink(ref))
	return out, nil
}

func (a *App) attachmentResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri, err := url.Parse(request.Params.URI)
	if err != nil {
		return nil, fmt.Errorf("Use an attachment URI returned by list_files")
	}
	parts := strings.Split(strings.Trim(uri.EscapedPath(), "/"), "/")
	if uri.Scheme != "memory" || uri.Host != "files" || (len(parts) != 2 && len(parts) != 4) {
		return nil, fmt.Errorf("Use an attachment URI returned by list_files")
	}
	id, e1 := url.PathUnescape(parts[0])
	name, e2 := url.PathUnescape(parts[1])
	if e1 != nil || e2 != nil {
		return nil, fmt.Errorf("Invalid attachment URI")
	}
	offset := int64(0)
	paged := len(parts) == 4
	if paged {
		if parts[2] != "pages" {
			return nil, fmt.Errorf("Use the pages_uri returned by list_files")
		}
		offset, err = strconv.ParseInt(parts[3], 10, 64)
		if err != nil || offset < 0 {
			return nil, fmt.Errorf("File page offset must be a nonnegative byte position")
		}
	}
	file, info, err := a.Store.OpenAttachment(ctx, id, name)
	if err != nil {
		return nil, fmt.Errorf("%s", clipError(err.Error()))
	}
	defer file.Close()
	size := int64(resourceFileLimit)
	if paged {
		size = resourcePageSize
	} else if info.Bytes > size {
		return nil, fmt.Errorf("Original is larger than 32 MiB. Read %s/pages/0 and follow next_uri to assemble its bytes", attachmentURI(id, name))
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > size {
		if !paged {
			return nil, fmt.Errorf("Original grew beyond 32 MiB; use its pages URI")
		}
		data = data[:size]
	}
	contents := &mcp.ResourceContents{URI: attachmentURI(id, name), MIMEType: info.MediaType, Blob: data}
	if paged {
		next := offset + int64(len(data))
		nextURI := ""
		if next < info.Bytes {
			nextURI = attachmentURI(id, name) + "/pages/" + strconv.FormatInt(next, 10)
		} else {
			next = 0
		}
		contents.URI = request.Params.URI
		contents.MIMEType = "application/octet-stream"
		contents.Meta = mcp.Meta{"filename": name, "mime_type": info.MediaType, "offset": offset, "next": next, "next_uri": nextURI, "total_bytes": info.Bytes}
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{contents}}, nil
}
