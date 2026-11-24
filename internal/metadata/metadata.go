package metadata

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
)

// Sidecar 是写入到媒体文件同目录的 JSON 元数据快照，兼容常见媒体库整理工具。
type Sidecar struct {
	Version        int                  `json:"version"`
	TweetID        string               `json:"tweetId"`
	TweetURL       string               `json:"tweetUrl"`
	AuthorID       string               `json:"authorId,omitempty"`
	AuthorName     string               `json:"authorName,omitempty"`
	AuthorHandle   string               `json:"authorScreenName,omitempty"`
	PublishedAt    time.Time            `json:"publishedAt"`
	Text           string               `json:"text"`
	MediaID        string               `json:"mediaId,omitempty"`
	MediaType      parser.MediaType     `json:"mediaType,omitempty"`
	MediaURL       string               `json:"mediaUrl,omitempty"`
	AltText        string               `json:"altText,omitempty"`
	ConversationID string               `json:"conversationId,omitempty"`
	Metrics        *parser.TweetMetrics `json:"metrics,omitempty"`
	ArchivedAt     time.Time            `json:"archivedAt"`
}

// WriteSidecarAndEmbed 将推文与媒体元数据写入伴随 .json 文件，并在支持的图片格式
// （JPEG / PNG）中无损嵌入标准 XMP 元数据（作者、正文、原链接、发布时间）。
// 任何格式不支持嵌入时静默跳过嵌入，但保留 JSON 伴随文件，确保不影响主下载流程。
func WriteSidecarAndEmbed(filePath string, tweet parser.TweetData, media parser.Media) error {
	if strings.TrimSpace(filePath) == "" {
		return nil
	}
	sidecar := Sidecar{
		Version:        1,
		TweetID:        tweet.ID,
		TweetURL:       tweet.URL,
		AuthorID:       tweet.Author.ID,
		AuthorName:     tweet.Author.Name,
		AuthorHandle:   tweet.Author.ScreenName,
		PublishedAt:    tweet.CreatedAt.UTC(),
		Text:           tweet.Text,
		MediaID:        media.ID,
		MediaType:      media.Type,
		MediaURL:       parser.MediaDownloadURL(media),
		AltText:        media.AltText,
		ConversationID: tweet.ConversationID,
		Metrics:        tweet.Metrics,
		ArchivedAt:     time.Now().UTC(),
	}
	if err := writeSidecarJSON(filePath+".json", sidecar); err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		xmp := buildXMPPacket(sidecar)
		if err := embedJPEGXMP(filePath, xmp, tweet.CreatedAt); err != nil {
			return err
		}
	case ".png":
		xmp := buildXMPPacket(sidecar)
		if err := embedPNGXMP(filePath, xmp, tweet.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func writeSidecarJSON(path string, sidecar Sidecar) error {
	data, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if !sidecar.PublishedAt.IsZero() {
		_ = os.Chtimes(path, sidecar.PublishedAt, sidecar.PublishedAt)
	}
	return nil
}

func escapeXMLText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func buildXMPPacket(s Sidecar) []byte {
	creator := strings.TrimSpace(s.AuthorName)
	if s.AuthorHandle != "" {
		if creator != "" {
			creator = fmt.Sprintf("%s (@%s)", creator, s.AuthorHandle)
		} else {
			creator = "@" + s.AuthorHandle
		}
	}
	desc := strings.TrimSpace(s.Text)
	if s.AltText != "" {
		if desc != "" {
			desc += "\n\nAlt: " + s.AltText
		} else {
			desc = s.AltText
		}
	}
	createDate := ""
	if !s.PublishedAt.IsZero() {
		createDate = s.PublishedAt.UTC().Format(time.RFC3339)
	}
	var b strings.Builder
	b.WriteString(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`)
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">`)
	b.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/">`)
	if creator != "" {
		b.WriteString(`<dc:creator><rdf:Seq><rdf:li>`)
		b.WriteString(escapeXMLText(creator))
		b.WriteString(`</rdf:li></rdf:Seq></dc:creator>`)
	}
	if desc != "" {
		b.WriteString(`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">`)
		b.WriteString(escapeXMLText(desc))
		b.WriteString(`</rdf:li></rdf:Alt></dc:description>`)
	}
	if s.TweetURL != "" {
		b.WriteString(`<dc:source>`)
		b.WriteString(escapeXMLText(s.TweetURL))
		b.WriteString(`</dc:source>`)
	}
	if s.TweetID != "" {
		b.WriteString(`<dc:identifier>`)
		b.WriteString(escapeXMLText("x-tweet:" + s.TweetID))
		b.WriteString(`</dc:identifier>`)
	}
	if createDate != "" {
		b.WriteString(`<xmp:CreateDate>`)
		b.WriteString(escapeXMLText(createDate))
		b.WriteString(`</xmp:CreateDate>`)
	}
	b.WriteString(`<xmp:CreatorTool>open-Xdownload</xmp:CreatorTool>`)
	b.WriteString(`</rdf:Description></rdf:RDF></x:xmpmeta>`)
	b.WriteString(`<?xpacket end="w"?>`)
	return []byte(b.String())
}

const jpegXMPHeader = "http://ns.adobe.com/xap/1.0/\x00"

// embedJPEGXMP 在 JPEG SOI(FFD8) 后插入或替换 XMP APP1 段（不重编码像素，完全无损）。
func embedJPEGXMP(filePath string, xmp []byte, modTime time.Time) error {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return nil // 非标准 JPEG，跳过
	}
	payloadLen := len(jpegXMPHeader) + len(xmp)
	if payloadLen+2 > 0xFFFF {
		return nil // 超出单 APP1 段 64KB 限制，跳过嵌入（JSON sidecar 已落盘）
	}
	var out bytes.Buffer
	out.Grow(len(raw) + payloadLen + 4)
	out.Write(raw[:2]) // SOI

	// 写入新 XMP APP1 段
	out.Write([]byte{0xFF, 0xE1})
	var segLen [2]byte
	binary.BigEndian.PutUint16(segLen[:], uint16(payloadLen+2))
	out.Write(segLen[:])
	out.WriteString(jpegXMPHeader)
	out.Write(xmp)

	// 拷贝剩余段，跳过已有的旧 XMP APP1 段以避免重复
	pos := 2
	for pos+1 < len(raw) {
		if raw[pos] != 0xFF {
			out.Write(raw[pos:])
			break
		}
		marker := raw[pos+1]
		// SOS(DA) 之后是熵编码图像数据，直接整体拷贝
		if marker == 0xDA || marker == 0xD9 {
			out.Write(raw[pos:])
			break
		}
		// 无长度参数的独立标记（RST0-7 / TEM / 填充 FF）
		if marker == 0xFF {
			out.WriteByte(0xFF)
			pos++
			continue
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			out.Write(raw[pos : pos+2])
			pos += 2
			continue
		}
		if pos+4 > len(raw) {
			out.Write(raw[pos:])
			break
		}
		length := int(binary.BigEndian.Uint16(raw[pos+2 : pos+4]))
		segEnd := pos + 2 + length
		if length < 2 || segEnd > len(raw) {
			out.Write(raw[pos:])
			break
		}
		isOldXMP := marker == 0xE1 && segEnd >= pos+4+len(jpegXMPHeader) &&
			string(raw[pos+4:pos+4+len(jpegXMPHeader)]) == jpegXMPHeader
		if !isOldXMP {
			out.Write(raw[pos:segEnd])
		}
		pos = segEnd
	}
	return atomicReplacePreservingTime(filePath, out.Bytes(), modTime)
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

const pngXMPKeyword = "XML:com.adobe.xmp"

// embedPNGXMP 在 PNG 的 IHDR 之后插入或替换标准 iTXt XMP chunk（完全无损）。
func embedPNGXMP(filePath string, xmp []byte, modTime time.Time) error {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	if len(raw) < 8 || !bytes.Equal(raw[:8], pngSignature) {
		return nil
	}
	// 构造 iTXt chunk data: keyword\0 + compressionFlag(0) + compressionMethod(0) + lang\0 + translated\0 + text
	var itxt bytes.Buffer
	itxt.WriteString(pngXMPKeyword)
	itxt.Write([]byte{0, 0, 0, 0, 0})
	itxt.Write(xmp)
	xmpChunk := buildPNGChunk("iTXt", itxt.Bytes())

	var out bytes.Buffer
	out.Grow(len(raw) + len(xmpChunk))
	out.Write(raw[:8])

	pos := 8
	inserted := false
	for pos+12 <= len(raw) {
		length := int(binary.BigEndian.Uint32(raw[pos : pos+4]))
		chunkEnd := pos + 12 + length
		if length < 0 || chunkEnd > len(raw) {
			return nil // 文件结构异常，保持原文件不动
		}
		chunkType := string(raw[pos+4 : pos+8])
		data := raw[pos+8 : pos+8+length]
		isOldXMP := chunkType == "iTXt" && bytes.HasPrefix(data, []byte(pngXMPKeyword+"\x00"))
		if !isOldXMP {
			out.Write(raw[pos:chunkEnd])
		}
		if chunkType == "IHDR" && !inserted {
			out.Write(xmpChunk)
			inserted = true
		}
		pos = chunkEnd
		if chunkType == "IEND" {
			break
		}
	}
	if !inserted {
		return nil
	}
	return atomicReplacePreservingTime(filePath, out.Bytes(), modTime)
}

func buildPNGChunk(chunkType string, data []byte) []byte {
	var buf bytes.Buffer
	var lenBytes [4]byte
	binary.BigEndian.PutUint32(lenBytes[:], uint32(len(data)))
	buf.Write(lenBytes[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(chunkType))
	_, _ = crc.Write(data)
	var crcBytes [4]byte
	binary.BigEndian.PutUint32(crcBytes[:], crc.Sum32())
	buf.Write(crcBytes[:])
	return buf.Bytes()
}

func atomicReplacePreservingTime(filePath string, data []byte, modTime time.Time) error {
	info, err := os.Stat(filePath)
	perm := os.FileMode(0o600)
	if err == nil {
		perm = info.Mode().Perm()
		if modTime.IsZero() {
			modTime = info.ModTime()
		}
	}
	tmp := filePath + ".xmp.tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, filePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if !modTime.IsZero() {
		_ = os.Chtimes(filePath, modTime, modTime)
	}
	return nil
}
