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
// SlackResponse: Slack에 보낼 기본 JSON 구조
// ─────────────────────────────────────────────────────
type SlackResponse struct {
	ResponseType string        `json:"response_type"`    // "in_channel" or "ephemeral"
	Blocks       []interface{} `json:"blocks,omitempty"` // Block Kit (이미지 삽입용)
	Text         string        `json:"text,omitempty"`   // 기본 텍스트 (fallback)
}

// ─────────────────────────────────────────────────────
// GitHubUser: GitHub REST API에서 받아올 사용자 정보 일부
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
// handleSlackStatus: /status {username} → GitHub 기본 정보 출력
// ─────────────────────────────────────────────────────
func handleSlackStatus(w http.ResponseWriter, r *http.Request) {
	// 1) POST가 아니면 405
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	// 2) 폼 파싱
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	// 3) Slack Verification Token 검증
	slackToken := r.FormValue("token")
	expectedToken := os.Getenv("SLACK_VERIFICATION_TOKEN")
	if expectedToken == "" {
		log.Println("⚠️ SLACK_VERIFICATION_TOKEN이 설정되어 있지 않습니다.")
	}
	if slackToken != expectedToken {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	// 4) 사용자명 읽기
	rawText := strings.TrimSpace(r.FormValue("text"))
	if rawText == "" {
		resp := SlackResponse{
			ResponseType: "ephemeral",
			Text:         "❗️ 사용자명을 입력해주세요.\n예: `/status octocat`",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
		return
	}
	username := strings.Fields(rawText)[0]

	// 5) GitHub API로 기본 정보 가져오기
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
	// 6) DisplayName 결정
	displayName := user.Name
	if displayName == "" {
		displayName = user.Login
	}
	// 7) 슬랙 메시지 텍스트 생성 (GitHubStatus 스타일)
	text := fmt.Sprintf(
		":bar_chart: *%s* (`%s`)\n"+
			"- :card_index_dividers: Public Repos: %d\n"+
			"- :busts_in_silhouette: Followers: %d\n"+
			"- :receipt: Bio: %s\n"+
			"- :link: <%s|GitHub 프로필 보기>",
		displayName,
		user.Login,
		user.PublicRepos,
		user.Followers,
		nullToEmpty(user.Bio),
		user.HTMLURL,
	)
	resp := SlackResponse{
		ResponseType: "in_channel",
		Text:         text,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// ─────────────────────────────────────────────────────
// handleSlackGrass: /grass {username} → GitHub 잔디 그래프
// ─────────────────────────────────────────────────────
func handleSlackGrass(w http.ResponseWriter, r *http.Request) {
	log.Println("[/grass] 요청 수신")

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		log.Printf("[/grass] Form 파싱 실패: %v\n", err)
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	slackToken := r.FormValue("token")
	expectedToken := os.Getenv("SLACK_VERIFICATION_TOKEN")
	if expectedToken == "" {
		log.Println("⚠️ SLACK_VERIFICATION_TOKEN이 설정되어 있지 않습니다.")
	}
	if slackToken != expectedToken {
		log.Printf("[/grass] 슬랙 토큰 불일치: 받은값=%s, 기대값=%s\n", slackToken, expectedToken)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	rawText := strings.TrimSpace(r.FormValue("text"))
	fields := strings.Fields(rawText)
	if len(fields) == 0 {
		resp := SlackResponse{
			ResponseType: "ephemeral",
			Text:         "❗️ 사용자명을 입력해주세요.\n예: `/grass octocat`",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
		return
	}
	username := fields[0]

	// 안전한 Block 구조체 정의
	type TextBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type SectionBlock struct {
		Type string     `json:"type"`
		Text *TextBlock `json:"text"`
	}
	type ImageBlock struct {
		Type     string `json:"type"`
		ImageURL string `json:"image_url"`
		AltText  string `json:"alt_text"`
	}
	/*
		// image_url: 슬랙에서 렌더링 가능한 PNG
		imageURL := fmt.Sprintf("https://ghchart.rshah.org/%s", username)

		// Block Kit 응답 구성

		blocks := []interface{}{
			SectionBlock{
				Type: "section",
				Text: &TextBlock{
					Type: "mrkdwn",
					Text: fmt.Sprintf("🌱 *%s* (`%s`) 님의 GitHub 잔디 그래프입니다:", username, username),
				},
			},
			ImageBlock{
				Type:     "image",
				ImageURL: imageURL,
				AltText:  "GitHub Contributions Graph",
			},
		}

			resp := SlackResponse{
				ResponseType: "in_channel",
				Blocks:       blocks,
			}
	*/
	resp := SlackResponse{
		ResponseType: "in_channel",
		Text:         fmt.Sprintf("🌱 *%s* 님의 GitHub 잔디 그래프 보기:\n<https://ghchart.rshah.org/%s>", username, username),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	log.Printf("[/grass] 응답 JSON: %+v\n", resp)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("[/grass] JSON 인코딩 실패: %v\n", err)
	}
}

// ─────────────────────────────────────────────────────
// handleContribs: /contribs?user={username} → GitHub 잔디 SVG를 그대로 프록시
// ─────────────────────────────────────────────────────
func handleContribs(w http.ResponseWriter, r *http.Request) {
	// user 파라미터 필수
	username := r.URL.Query().Get("user")
	if username == "" {
		http.Error(w, "user query parameter is required", http.StatusBadRequest)
		return
	}
	// GitHub 잔디 그래프 페이지 (SVG)
	url := fmt.Sprintf("https://github.com/users/%s/contributions", username)
	resp, err := http.Get(url)
	if err != nil {
		http.Error(w, "Failed to fetch contributions", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("GitHub 응답 코드 %d: %s", resp.StatusCode, string(body)), http.StatusBadGateway)
		return
	}
	// Content-Type: image/svg+xml 으로 설정 후, 그대로 복사
	w.Header().Set("Content-Type", "image/svg+xml")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
}

// ─────────────────────────────────────────────────────
// fetchGitHubUser: GitHub REST API에서 사용자 정보 가져오기
// ─────────────────────────────────────────────────────
func fetchGitHubUser(username string) (*GitHubUser, error) {
	url := fmt.Sprintf("https://api.github.com/users/%s", username)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// GITHUB_TOKEN이 있으면 인증용 헤더로 사용
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("User-Agent", "Go-Slack-GitHub-Bot")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub 응답 코드 %d: %s", resp.StatusCode, string(body))
	}
	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

// ─────────────────────────────────────────────────────
// nullToEmpty: Bio가 빈 문자열일 때 대체 텍스트
// ─────────────────────────────────────────────────────
func nullToEmpty(s string) string {
	if s == "" {
		return "no bio"
	}
	return s
}

// ─────────────────────────────────────────────────────
// main: HTTP 서버 시작, 핸들러 등록
// ─────────────────────────────────────────────────────
func main() {
	// 환경변수 SLACK_VERIFICATION_TOKEN 로그
	if os.Getenv("SLACK_VERIFICATION_TOKEN") == "" {
		log.Println("⚠️ SLACK_VERIFICATION_TOKEN이 설정되어 있지 않습니다. 토큰 검증을 건너뜁니다.")
	}

	mux := http.NewServeMux()
	// /status  → GitHub 기본 정보 (GitHubStatus 스타일)
	mux.HandleFunc("/status", handleSlackStatus)
	// /grass   → GitHub 잔디 그래프 (SVG 포함)
	mux.HandleFunc("/grass", handleSlackGrass)
	// /contribs → 실제 SVG를 Slack이 가져갈 수 있게 proxy
	mux.HandleFunc("/contribs", handleContribs)

	port := "8080"
	fmt.Printf("서버 실행 중... http://localhost:%s/{status,grass,contribs}\n", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
