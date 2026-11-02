// Package store 负责课堂会话的持久化。
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Segment 是一页幻灯片的笔记单元。
type Segment struct {
	ID           string    `json:"id"`
	Index        int       `json:"index"`      // 第几页（按本堂课出现顺序）
	SlideIndex   int       `json:"slideIndex"` // PPT 原始页码
	CreatedAt    time.Time `json:"createdAt"`
	ImageFile    string    `json:"imageFile"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	SHA          string    `json:"sha"`
	Transcript   string    `json:"transcript"`
	Notes        string    `json:"notes"`
	Summary      string    `json:"summary"`
	BoardText    string    `json:"boardText"`
	KeyPoints    []string  `json:"keyPoints"`
	Tips         []string  `json:"tips"`
	Copying      bool      `json:"copying"`
	CopyScore    int       `json:"copyScore"`
	CopyEvidence string    `json:"copyEvidence"`
	Status       string    `json:"status"` // pending | analyzing | done | failed
	Model        string    `json:"model"`
	Tokens       int       `json:"tokens"`
	Error        string    `json:"error,omitempty"`
	Duration     int       `json:"durationSec"`
}

// ChatMessage 是课堂内的对话消息。
type ChatMessage struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

// Report 是课堂报告。
type Report struct {
	CreatedAt time.Time `json:"createdAt"`
	HTMLPath  string    `json:"htmlPath"`
	PDFPath   string    `json:"pdfPath"`
	Copying   bool      `json:"copying"`
	CopyRate  int       `json:"copyRate"`
	Summary   string    `json:"summary"`
	Title     string    `json:"title"`
}

// Stats 是课堂统计。
type Stats struct {
	SlideCount  int `json:"slideCount"`
	Words       int `json:"words"`
	DurationSec int `json:"durationSec"`
	AICalls     int `json:"aiCalls"`
	Tokens      int `json:"tokens"`
}

// Session 是一堂课的完整记录。
type Session struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Subject   string        `json:"subject"`
	Room      string        `json:"room,omitempty"`
	Teacher   string        `json:"teacher,omitempty"`
	Source    string        `json:"source"` // classisland | manual
	StartedAt time.Time     `json:"startedAt"`
	EndedAt   *time.Time    `json:"endedAt,omitempty"`
	Status    string        `json:"status"` // recording | ended
	Segments  []*Segment    `json:"segments"`
	Messages  []ChatMessage `json:"messages"`
	Report    *Report       `json:"report,omitempty"`
	Stats     Stats         `json:"stats"`
	AIModel   string        `json:"aiModel"`
}

// Meta 是会话列表项。
type Meta struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Subject   string     `json:"subject"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Status    string     `json:"status"`
	Slides    int        `json:"slides"`
	Words     int        `json:"words"`
	HasReport bool       `json:"hasReport"`
}

// Store 管理会话目录。
type Store struct {
	dir string
	mu  sync.Mutex
}

// New 创建存储。
func New(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

// Dir 返回会话目录。
func (s *Store) Dir() string { return s.dir }

// NewID 生成会话 ID。
func NewID(t time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return t.Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Save 写入会话。
func (s *Store) Save(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(sess)
}

func (s *Store) save(sess *Session) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	sess.Stats.SlideCount = len(sess.Segments)
	words := 0
	for _, seg := range sess.Segments {
		words += len([]rune(seg.Transcript))
	}
	sess.Stats.Words = words
	raw, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, sess.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AppendMessage 往历史课堂里追加一条消息。
//
// 只用于已经结束的课堂：正在记录的课堂由 lesson.Engine 持锁写入，
// 这里 Load+Save 会覆盖掉并发落盘的片段。
func (s *Store) AppendMessage(id, role, content string) error {
	sess, err := s.Load(id)
	if err != nil {
		return err
	}
	if sess.Status == "recording" {
		return fmt.Errorf("课堂正在记录，请由引擎写入")
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		ID: NewID(time.Now()), Role: role, Content: content, CreatedAt: time.Now(),
	})
	return s.Save(sess)
}

// checkID 校验会话 ID 能否安全地拼进文件名。
//
// 会话 ID 由程序生成（时间戳形态），所以这里只放行「字母数字 + - _ .」，
// 并额外挡掉单独的 ".."：这样 Windows 与 Linux 上都不会有拼接歧义。
func checkID(id string) error {
	if id == "" || id == "." || id == ".." || len(id) > 96 {
		return fmt.Errorf("非法的会话 ID")
	}
	for _, r := range id {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return fmt.Errorf("非法的会话 ID")
		}
	}
	return nil
}

// Load 读取会话。
func (s *Store) Load(id string) (*Session, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

// List 返回所有会话（按开始时间倒序）。
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var sess Session
		if err := json.Unmarshal(raw, &sess); err != nil {
			continue
		}
		out = append(out, Meta{
			ID: sess.ID, Title: sess.Title, Subject: sess.Subject,
			StartedAt: sess.StartedAt, EndedAt: sess.EndedAt, Status: sess.Status,
			Slides: len(sess.Segments), Words: sess.Stats.Words, HasReport: sess.Report != nil,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// Delete 删除会话记录（截图目录保留，历史页面里已导出的图片仍可打开）。
func (s *Store) Delete(id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.dir, id+".json"))
	return nil
}
