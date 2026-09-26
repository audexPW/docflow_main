package recognize

// Повтор распознавания по сохранённой трассировке — без OCR и без модели.
//
//	DOCFLOW_REPLAY=/opt/docflow-deploy/trace go test ./internal/recognize -run TestReplayTraces -v
//
// Из каталога прогона берутся слова с координатами (05), текст (03/04),
// сетка surya (06) и записанные ответы модели (11, 13). Модель подменяется
// локальным сервером, который отдаёт эти ответы. Так правку сборки таблицы
// или выбора номера можно проверить на всей партии за секунды, не гоняя
// документы через OCR и Qwen заново. Без DOCFLOW_REPLAY тест пропускается.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"docflow/internal/config"
	"docflow/internal/domain"
)

type replayCase struct {
	Dir      string
	Page     pageText
	Fields   string
	Table    string
	Hint     ProcessHint
	Recorded domain.Recognition
	Env      map[string]string
}

// applyReplayEnv выставляет настройки прогона из трассировки. Пути /etc/docflow
// заменяются на файлы из корня docflow-deploy.
func applyReplayEnv(t *testing.T, env map[string]string) {
	for k, v := range env {
		if strings.HasPrefix(v, "/etc/docflow/") {
			local := filepath.Join("..", "..", "..", strings.TrimPrefix(v, "/etc/docflow/"))
			if _, err := os.Stat(local); err != nil {
				continue
			}
			v = local
		}
		t.Setenv(k, v)
	}
	prev := primaryTaxKey
	t.Cleanup(func() { primaryTaxKey = prev })
	if loc := env["LOCALE"]; loc != "" {
		SetLocale(loc)
	}
	if p := os.Getenv("DOCTYPES_PATH"); p != "" && !strings.HasPrefix(p, "/etc/") {
		_, _ = LoadDocTypes(p)
	}
	if p := os.Getenv("ACCOUNTS_PATH"); p != "" && !strings.HasPrefix(p, "/etc/") {
		_ = LoadAccounts(p)
	}
}

func readTraceFile(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func loadReplayCase(dir string) (replayCase, bool) {
	rc := replayCase{Dir: dir}
	f, err := os.Open(filepath.Join(dir, "05-ocr-words.tsv"))
	if err != nil {
		return rc, false
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for first := true; sc.Scan(); first = false {
		if first {
			continue
		}
		parts := strings.SplitN(sc.Text(), "\t", 5)
		if len(parts) < 5 {
			continue
		}
		num := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
		rc.Page.Words = append(rc.Page.Words, wordBox{Text: parts[4], X0: num(parts[0]), Y0: num(parts[1]), X1: num(parts[2]), Y1: num(parts[3])})
	}
	f.Close()
	rc.Page.Flat = readTraceFile(dir, "03-ocr-flat.txt")
	rc.Page.Layout = readTraceFile(dir, "04-ocr-layout-engine.txt")
	if rc.Page.Layout == "" {
		rc.Page.Layout = readTraceFile(dir, "04-ocr-layout.txt")
	}
	_ = json.Unmarshal([]byte(readTraceFile(dir, "06-ocr-grids.json")), &rc.Page.Grids)
	rc.Fields = readTraceFile(dir, "11-llm-fields-answer.txt")
	rc.Table = readTraceFile(dir, "13-llm-table-answer.txt")
	var meta map[string]any
	_ = json.Unmarshal([]byte(readTraceFile(dir, "00-meta.json")), &meta)
	if unp, _ := meta["компания_унп"].(string); unp != "" {
		name, _ := meta["компания"].(string)
		rc.Hint.Company = &CompanyHint{Name: name, UNP: unp}
	}
	_ = json.Unmarshal([]byte(readTraceFile(dir, "16-final.json")), &rc.Recorded)
	if env, ok := meta["настройки"].(map[string]any); ok {
		rc.Env = map[string]string{}
		for k, v := range env {
			if sv, ok := v.(string); ok {
				rc.Env[k] = sv
			}
		}
	}
	return rc, len(rc.Page.Words) > 0 && strings.TrimSpace(rc.Page.Flat) != ""
}

// replayModel отдаёт записанный ответ: запрос табличной части узнаётся по
// системному промпту.
func replayModel(rc *replayCase) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		_ = json.Unmarshal(body, &req)
		answer := rc.Fields
		for _, m := range req.Messages {
			if m.Role == "system" && strings.Contains(m.Content, "табличную часть") {
				answer = rc.Table
			}
		}
		if strings.TrimSpace(answer) == "" {
			http.Error(w, "нет записанного ответа", http.StatusInternalServerError)
			return
		}
		var resp chatResponse
		resp.Choices = append(resp.Choices, struct {
			Message chatMessage `json:"message"`
		}{Message: chatMessage{Role: "assistant", Content: answer}})
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func replayOne(t *testing.T, rc *replayCase) domain.Recognition {
	srv := replayModel(rc)
	defer srv.Close()
	p := &Pipeline{
		cfg:   config.Config{LLM: config.LLMConfig{Enabled: true, BaseURL: srv.URL, Model: "replay", Timeout: 10 * time.Second, MaxTokens: 2048}},
		model: newModelClient(config.LLMConfig{Enabled: true, BaseURL: srv.URL, Model: "replay", Timeout: 10 * time.Second, MaxTokens: 2048}),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	res, err := p.analyzePage(context.Background(), rc.Page, nil, rc.Hint)
	if err != nil {
		t.Logf("%s: %v", filepath.Base(rc.Dir), err)
	}
	return res.Recognition
}

func linesSumString(rec domain.Recognition) string {
	sum, bad := 0.0, 0
	for _, l := range rec.Lines {
		if v, err := parseMoney(l.Amount); err == nil {
			sum += v
		} else {
			bad++
		}
	}
	s := strconv.FormatFloat(math.Round(sum*100)/100, 'f', 2, 64)
	if bad > 0 {
		s += fmt.Sprintf("(+%d без суммы)", bad)
	}
	return s
}

func TestReplayTraces(t *testing.T) {
	root := os.Getenv("DOCFLOW_REPLAY")
	if root == "" {
		t.Skip("DOCFLOW_REPLAY не задан")
	}
	dirs, _ := filepath.Glob(filepath.Join(root, "2*"))
	sort.Strings(dirs)
	var total, linesBad, linesBadBefore, faulty, faultyBefore int
	for _, dir := range dirs {
		rc, ok := loadReplayCase(dir)
		if !ok {
			continue
		}
		total++
		var got domain.Recognition
		t.Run(filepath.Base(dir), func(t *testing.T) {
			applyReplayEnv(t, rc.Env)
			got = replayOne(t, &rc)
		})
		before := LinesTotalMismatch(rc.Recorded, LinesTolerance)
		after := LinesTotalMismatch(got, LinesTolerance)
		if before {
			linesBadBefore++
		}
		if after {
			linesBad++
		}
		if RecognitionFaulty(rc.Recorded) {
			faultyBefore++
		}
		if RecognitionFaulty(got) {
			faulty++
		}
		if show := os.Getenv("DOCFLOW_REPLAY_SHOW"); show != "" && strings.Contains(dir, show) && got.Table != nil && got.Table.RawCells != nil {
			ft := got.Table.RawCells
			t.Logf("%s: источник %s, графы %q роли %q", filepath.Base(dir), ft.Source, ft.Columns, ft.Roles)
			for _, r := range ft.Rows {
				t.Logf("   | %s", strings.Join(r, " | "))
			}
			for _, l := range got.Lines {
				t.Logf("   L %+v", l)
			}
			for _, k := range []string{"amount_no_vat", "vat_amount", "total"} {
				t.Logf("   F %s=%+v note=%q", k, got.Fields[k], got.ReviewNotes[k])
			}
		}
		t.Logf("%-34s итог=%-8s строки было=%d/%s стало=%d/%s сходится было=%v стало=%v | номер было=%q стало=%q | ошибки %v пропуски %v",
			filepath.Base(dir), got.Fields["total"].Value,
			len(rc.Recorded.Lines), linesSumString(rc.Recorded), len(got.Lines), linesSumString(got),
			!before, !after, rc.Recorded.Fields["number"].Value, got.Fields["number"].Value, got.Faults, got.Missing)
	}
	t.Logf("документов %d; позиции не сходятся с итогом: было %d, стало %d; с ошибками распознавания: было %d, стало %d",
		total, linesBadBefore, linesBad, faultyBefore, faulty)
}
