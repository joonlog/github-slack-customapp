package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type SlackResponse struct {
	ResponseType string        `json:"response_type"`
	Blocks       []interface{} `json:"blocks,omitempty"`
	Text         string        `json:"text,omitempty"`
}

type GitHubUser struct {
	Login       string `json:"login"`
	Name        string `json:"name"`
	PublicRepos int    `json:"public_repos"`
	Followers   int    `json:"followers"`
	Bio         string `json:"bio"`
	HTMLURL     string `json:"html_url"`
}

func handleSlackStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	slackToken := r.FormValue("token")
	expectedToken := os.Getenv("SLACK_VERIFICATION_TOKEN")
	if expectedToken == "" {
		log.Println("⚠️ SLACK_VERIFICATION_TOKEN이 설정되어 있지 않습니다.")
	}
	if slackToken != expectedToken {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
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
	displayName := user.Name
	if displayName == "" {
		displayName = user.Login
	}
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

func fetchGitHubUser(username string) (*GitHubUser, error) {
	url := fmt.Sprintf("https://api.github.com/users/%s", username)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
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

func nullToEmpty(s string) string {
	if s == "" {
		return "no bio"
	}
	return s
}

func handleSlackGrass(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	rawText := strings.TrimSpace(r.FormValue("text"))
	log.Printf("🔤 Slack 입력 text: '%s'", rawText)

	parts := strings.Fields(rawText)
	if len(parts) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SlackResponse{
			ResponseType: "ephemeral",
			Text:         "❗️ 사용자명을 입력해주세요.\n예: `/grass joonlog`",
		})
		return
	}

	username := parts[0]
	log.Printf("🌱 /grass 요청 수신됨 - username: %s", username)

	svgURL := fmt.Sprintf("https://ghchart.rshah.org/%s", username)
	resp, err := http.Get(svgURL)
	if err != nil || resp.StatusCode != 200 {
		log.Printf("SVG 요청 실패: %v", err)
		http.Error(w, "GitHub chart를 가져오지 못했습니다.", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// SVG 저장
	svgData, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "SVG 읽기 실패", http.StatusInternalServerError)
		return
	}
	tmpSvg := fmt.Sprintf("/tmp/%s.svg", username)
	if err := os.WriteFile(tmpSvg, svgData, 0644); err != nil {
		http.Error(w, "SVG 저장 실패", http.StatusInternalServerError)
		return
	}

	// PNG로 변환 (rsvg-convert 필요)
	tmpPng := fmt.Sprintf("/tmp/%s.png", username)
	cmd := exec.Command("rsvg-convert", "-o", tmpPng, tmpSvg)
	if err := cmd.Run(); err != nil {
		http.Error(w, "PNG 변환 실패", http.StatusInternalServerError)
		return
	}

	// 슬랙에 PNG 업로드는 아직 안함 — 여기까지는 파일 생성까지만
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SlackResponse{
		ResponseType: "ephemeral",
		Text:         fmt.Sprintf("✅ SVG → PNG 변환 성공: %s", tmpPng),
	})
}

func main() {
	if os.Getenv("SLACK_VERIFICATION_TOKEN") == "" {
		log.Println("⚠️ SLACK_VERIFICATION_TOKEN이 설정되어 있지 않습니다. 토큰 검증을 건너뜁니다.")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", handleSlackStatus)
	mux.HandleFunc("/grass", handleSlackGrass)

	port := "8080"
	fmt.Printf("서버 실행 중... http://localhost:%s/{status,grass}\n", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
