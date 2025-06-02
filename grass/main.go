package main

import (
	"bytes"
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

type SlackUploadURLResponse struct {
	OK    bool `json:"ok"`
	Files []struct {
		ID         string `json:"id"`
		UploadURL  string `json:"upload_url"`
		URLPrivate string `json:"url_private"`
	} `json:"files"`
}

func handleSlackGrass(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		log.Printf("❌ Form 파싱 실패: %v", err)
		http.Error(w, "form parse error", http.StatusBadRequest)
		return
	}

	rawText := strings.TrimSpace(r.FormValue("text"))
	log.Printf("📥 Slash command form: text='%s', channel_id='%s', token='%s'", r.FormValue("text"), r.FormValue("channel_id"), r.FormValue("token"))
	parts := strings.Fields(rawText)
	if len(parts) == 0 {
		log.Println("✅ Slack slash command 응답 전송 중")
		writeSlackJSON(w, SlackResponse{
			ResponseType: "ephemeral",
			Text:         "❗ 사용자명을 입력해주세요. 예: `/grass joonlog`",
		})
		return
	}
	username := parts[0]
	log.Printf("\U0001F331 /grass 요청 수신됨 - username: %s", username)

	svgURL := fmt.Sprintf("https://ghchart.rshah.org/%s", username)
	resp, err := http.Get(svgURL)
	if err != nil || resp.StatusCode != 200 {
		log.Printf("SVG 요청 실패: %v", err)
		http.Error(w, "SVG 요청 실패", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	svgData, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "SVG 읽기 실패", http.StatusInternalServerError)
		return
	}
	tmpSvg := fmt.Sprintf("/tmp/%s.svg", username)
	tmpPng := fmt.Sprintf("/tmp/%s.png", username)
	os.WriteFile(tmpSvg, svgData, 0644)

	if err := exec.Command("rsvg-convert", "-o", tmpPng, tmpSvg).Run(); err != nil {
		http.Error(w, "PNG 변환 실패", http.StatusInternalServerError)
		return
	}

	slackToken := os.Getenv("SLACK_BOT_TOKEN")
	size := fileSize(tmpPng)
	filename := fmt.Sprintf("%s_contributions.png", username)
	log.Printf("\U0001F4E6 업로드 요청 - filename: %s, size: %d", filename, size)

	uploadReq := map[string]interface{}{
		"files": []map[string]interface{}{
			{
				"filename": filename,
				"length":   size,
			},
		},
		"channels": []string{r.FormValue("channel_id")},
	}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(uploadReq); err != nil {
		log.Printf("❌ JSON 인코딩 실패: %v", err)
		http.Error(w, "JSON encode error", http.StatusInternalServerError)
		return
	}
	log.Printf("📤 최종 JSON body: %s", buf.String())

	req, err := http.NewRequest("POST", "https://slack.com/api/files.getUploadURLExternal", io.NopCloser(buf)) // ✅ io.NopCloser
	if err != nil {
		log.Printf("❌ request 생성 실패: %v", err)
		http.Error(w, "request 생성 실패", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+slackToken)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(buf.Len()) // ✅ 강제 설정

	client := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: false, // ✅ HTTP/1.1 강제
		},
		Timeout: 5 * time.Second,
	}
	uploadResp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Slack 업로드 URL 요청 실패", http.StatusInternalServerError)
		return
	}
	defer uploadResp.Body.Close()
	responseData, _ := io.ReadAll(uploadResp.Body)
	log.Printf("Slack 업로드 응답: %s", string(responseData))

	var uploadURLResp SlackUploadURLResponse
	if err := json.Unmarshal(responseData, &uploadURLResp); err != nil {
		log.Printf("❌ JSON 파싱 실패: %v", err)
		http.Error(w, "Slack 업로드 URL 파싱 실패", http.StatusInternalServerError)
		return
	}
	log.Printf("✅ Slack 업로드 응답 파싱 완료: ok=%v, files=%d", uploadURLResp.OK, len(uploadURLResp.Files))
	if !uploadURLResp.OK || len(uploadURLResp.Files) == 0 {
		log.Printf("❌ Slack 응답 오류 - OK: %v, Files: %v", uploadURLResp.OK, uploadURLResp.Files)
		http.Error(w, "Slack 업로드 응답 형식 오류", http.StatusInternalServerError)
		return
	}

	uploadURL := uploadURLResp.Files[0].UploadURL
	fileID := uploadURLResp.Files[0].ID

	log.Println("📤 파일 PUT 업로드 시작")
	if err := uploadFileToSlack(uploadURL, tmpPng); err != nil {
		log.Printf("❌ Slack 업로드 실패: %v", err)
		http.Error(w, "Slack 업로드 실패", http.StatusInternalServerError)
		return
	}
	log.Println("✅ 파일 업로드 성공")

	completePayload := map[string]interface{}{
		"files": []map[string]string{
			{"id": fileID},
		},
	}
	log.Printf("📬 completeUploadExternal payload: %+v", completePayload)

	req, _ = http.NewRequest("POST", "https://slack.com/api/files.completeUploadExternal", toJSONBody(completePayload))
	req.Header.Set("Authorization", "Bearer "+slackToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err = client.Do(req)
	if err != nil {
		log.Printf("❌ chat.postMessage 전송 실패: %v", err)
		http.Error(w, "Slack 메시지 전송 실패", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("📬 chat.postMessage 응답: %s", string(body))

	// 채널로 직접 이미지 전송
	channelID := r.FormValue("channel_id")
	postPayload := map[string]interface{}{
		"channel":  channelID,
		"text":     fmt.Sprintf("\U0001F331 *%s*님의 GitHub 잔디 현황입니다!", username),
		"file_ids": []string{fileID},
	}
	log.Printf("🚀 chat.postMessage 시작 - channel: %s, file_id: %s", channelID, fileID)
	req, _ = http.NewRequest("POST", "https://slack.com/api/chat.postMessage", toJSONBody(postPayload))
	req.Header.Set("Authorization", "Bearer "+slackToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err = client.Do(req)
	if err != nil {
		log.Printf("❌ chat.postMessage 전송 실패: %v", err)
		http.Error(w, "Slack 메시지 전송 실패", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)
	log.Printf("📬 chat.postMessage 응답: %s", string(body))
	if err != nil {
		http.Error(w, "Slack 메시지 전송 실패", http.StatusInternalServerError)
		return
	}

	// 슬래시 응답은 따로 필요 없음 (이미 chat.postMessage로 전송됨)
	log.Println("✅ Slack slash command 응답 전송 중")
	writeSlackJSON(w, SlackResponse{
		ResponseType: "ephemeral",
		Text:         fmt.Sprintf("🌱 *%s*님의 잔디 이미지가 전송되었습니다.", username),
	})
}

func toJSONBody(obj interface{}) io.Reader {
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(obj); err != nil {
		log.Printf("❌ JSON 인코딩 실패: %v", err)
		return nil // 여기서 중단
	}
	log.Printf("📤 Slack API 전송 바디: %s", buf.String())
	return buf
}

func writeSlackJSON(w http.ResponseWriter, payload SlackResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(payload)
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		log.Printf("⚠️ 파일 크기 가져오기 실패: %v", err)
		return 0
	}
	return info.Size()
}

func uploadFileToSlack(uploadURL, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	req, err := http.NewRequest("PUT", uploadURL, file)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Slack 파일 업로드 실패: %s", string(body))
	}
	return nil
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
