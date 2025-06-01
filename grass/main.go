package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────
// 1) GitHubUser: GitHub API에서 받아올 사용자 정보 일부
// ─────────────────────────────────────────────────────
type GitHubUser struct {
	Login       string `json:"login"`
	Name        string `json:"name"`
	PublicRepos int    `json:"public_repos"`
	Followers   int    `json:"followers"`
	Bio         string `json:"bio"`
	HTMLURL     string `json:"html_url"`
}

// ─────────────────────────────────────────────────────
// 2) SlackResponse: 슬랙으로 응답할 JSON 형식
// ─────────────────────────────────────────────────────
type SlackResponse struct {
	ResponseType string `json:"response_type"` // "in_channel" or "ephemeral"
	Text         string `json:"text"`          // 본문 텍스트 (Markdown 가능)
	// Attachments  []interface{} `json:"attachments,omitempty"` // 필요시 확장
}

// ─────────────────────────────────────────────────────
// 3) handleSlackCommand: 슬랙 Slash Command 요청 처리
// ─────────────────────────────────────────────────────
func handleSlackCommand(w http.ResponseWriter, r *http.Request) {
	// 3.1) POST가 아니면 405
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 3.2) 폼 파싱
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// 3.3) Slack Verification Token 확인 (보안을 위해 반드시 넣을 것)
	slackToken := r.FormValue("token")
	expectedToken := os.Getenv("SLACK_VERIFICATION_TOKEN")
	if expectedToken == "" {
		log.Println("⚠️SLACK_VERIFICATION_TOKEN 환경변수가 설정되지 않았습니다.")
	}
	if slackToken != expectedToken {
		// 토큰이 일치하지 않으면 Unauthorized
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 3.4) 사용자 입력(텍스트) 가져오기
	rawText := strings.TrimSpace(r.FormValue("text"))
	if rawText == "" {
		// 예시 응답: “사용자명을 입력해주세요.”
		resp := SlackResponse{
			ResponseType: "ephemeral",
			Text:         "❗️ 사용자명을 입력해주세요.\n예: `/grass octocat`",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// 3.5) “텍스트를 나눠서” 복수 사용자 지원 여지 열어두기
	args := strings.Fields(rawText)
	// 현재는 첫 번째 인자만 처리 (확장 시 모든 args 순회 가능)
	username := args[0]

	// 3.6) GitHub User 정보 조회 (Rate Limit 해제용 토큰 사용 옵션)
	user, err := fetchGitHubUser(username)
	if err != nil {
		resp := SlackResponse{
			ResponseType: "ephemeral",
			Text:         fmt.Sprintf("❌ 사용자 `%s` 정보를 불러올 수 없습니다.\n> %v", username, err),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
		return
	}

	// 3.7) DisplayName 결정 (Name이 비어 있으면 Login으로 대체)
	displayName := user.Name
	if displayName == "" {
		displayName = user.Login
	}

	// 3.8) Slack 메시지 본문 구성 (Markdown 사용 가능)
	text := fmt.Sprintf(
		"📊 *%s* (`%s`)\n"+
			"- 🗂 *Public Repos:* %d\n"+
			"- 👥 *Followers:* %d\n"+
			"- 🧾 *Bio:* %s\n"+
			"- 🔗 <%s|GitHub 프로필 보기>",
		displayName,
		user.Login,
		user.PublicRepos,
		user.Followers,
		nullToEmpty(user.Bio),
		user.HTMLURL,
	)

	// 3.9) Slack에 응답 (in_channel: 채널 공개, ephemeral: 본인만 보기)
	resp := SlackResponse{
		ResponseType: "in_channel",
		Text:         text,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// ─────────────────────────────────────────────────────
// 4) fetchGitHubUser: GitHub API로부터 사용자 정보 받아오기
// ─────────────────────────────────────────────────────
func fetchGitHubUser(username string) (*GitHubUser, error) {
	// 4.1) 기본 URL. username에 공백/특수문자 들어오면 그대로 URL 인코딩 하지 않도록 주의
	url := fmt.Sprintf("https://api.github.com/users/%s", username)

	// 4.2) HTTP 클라이언트 생성 및 요청 객체 만들기 (Rate Limit 해제용 토큰 사용 가능)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// 4.3) 환경변수에 GITHUB_TOKEN이 설정되어 있으면, Authorization 헤더에 추가
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}

	// 4.4) User-Agent 헤더 추가 권고 (GitHub API 정책 상 필요)
	req.Header.Set("User-Agent", "Go-Slack-GitHub-Bot")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 4.5) StatusCode 검사
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub 응답 코드 %d: %s", resp.StatusCode, string(body))
	}

	// 4.6) JSON 파싱
	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

// ─────────────────────────────────────────────────────
// 5) nullToEmpty: Bio가 비어 있을 때 대체 텍스트
// ─────────────────────────────────────────────────────
func nullToEmpty(s string) string {
	if s == "" {
		return "_no bio_" // Markdown 이탤릭 표시
	}
	return s
}

// ─────────────────────────────────────────────────────
// 6) main: HTTP 서버 시작
// ─────────────────────────────────────────────────────
func main() {
	// 6.1) 환경변수 체크 (개발 중 누락되었는지 로그로 확인)
	if os.Getenv("SLACK_VERIFICATION_TOKEN") == "" {
		log.Println("⚠️ SLACK_VERIFICATION_TOKEN이 설정되어 있지 않습니다. 토큰 검증은 건너뛰어집니다.")
	}

	// 6.2) 핸들러 등록
	http.HandleFunc("/grass", handleSlackCommand)

	// 6.3) 서버 시작
	addr := ":8080"
	fmt.Printf("서버 실행 중... http://localhost%s/grass (POST)\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
