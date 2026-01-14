package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// [설정] Gzip 압축 사용 여부 (false로 설정 시 압축 비활성화)
const EnableGzip = true

// 설정 정보를 담을 구조체
type Config struct {
	Systems map[string]string // coreMap
}

type SyncInfo struct {
	LastSyncTime int64 `json:"lastSyncTime"`
}

type BookmarkItem struct {
	System string `json:"system"`
	Rom    string `json:"rom"`
}

type RomInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type InjectLog map[string]string

var (
	processingMutex sync.Mutex
	processingFiles = make(map[string]bool)
)

// [SSR 캐시] 완성된 HTML 페이지를 캐싱
var indexCache struct {
	sync.RWMutex
	Content       []byte    // Gzip 압축된 HTML (최적화)
	RawContent    []byte    // 압축되지 않은 HTML (Gzip 미지원 브라우저용)
	RomsDirTime   time.Time
	IndexFileTime time.Time
	IndexFileSize int64     // [추가] index.html 파일 크기 (변경 감지용)
	ETag          string
}

// index.html에서 설정을 읽어옴
func loadConfigFromHTML() Config {
	config := Config{Systems: make(map[string]string)}
	file, err := os.Open("index.html")
	if err != nil { return config }
	defer file.Close()
	scanner := bufio.NewScanner(file)
	inMap := false
	reStart := regexp.MustCompile(`const coreMap = \{`)
	reEnd := regexp.MustCompile(`\};`)
	reItem := regexp.MustCompile(`(\w+):\s*"([^"]+)"`)
	for scanner.Scan() {
		line := scanner.Text()
		if reStart.MatchString(line) { inMap = true; continue }
		if inMap && reEnd.MatchString(line) { break }
		if inMap {
			matches := reItem.FindStringSubmatch(line)
			if len(matches) == 3 { config.Systems[matches[1]] = matches[2] }
		}
	}
	return config
}

func getDiskUsage(path string) (uint64, uint64) {
	var stat syscall.Statfs_t
	syscall.Statfs(path, &stat)
	free := stat.Bavail * uint64(stat.Bsize)
	total := stat.Blocks * uint64(stat.Bsize)
	return free, total
}

func handleDiskInfo(w http.ResponseWriter, r *http.Request) {
	wd, _ := os.Getwd()
	free, total := getDiskUsage(wd)
	json.NewEncoder(w).Encode(map[string]uint64{"free": free, "total": total})
}

// [수정] 카드 HTML 생성: Cover 방식으로 로직 변경
func writeCardHTML(sb *strings.Builder, sys, romName string, size int64) {
	romNameDisp := strings.TrimSuffix(romName, filepath.Ext(romName))
	romNameDisp = strings.ToUpper(romNameDisp)
	
	// 사이즈 게이지 계산
	// 최대 20MB를 100%로 가정
	mb := float64(size) / (1024 * 1024)
	percent := math.Min(100, (mb/20.0)*100)
	if percent < 5 { percent = 5 } // 최소값

	// [중요] 가림막(Cover)의 너비를 계산
	// 파일 크기(percent)가 100%면 Cover는 0%가 되어 전체 그라데이션이 보임
	// 파일 크기가 10%면 Cover는 90%가 되어 오른쪽 10%만 보임
	coverPercent := 100 - percent

	// 따옴표(') 처리
	safeSys := strings.ReplaceAll(sys, "'", "\\'")
	safeRom := strings.ReplaceAll(romName, "'", "\\'")

	// 구조: .size-gauge(배경 그라데이션) > .gauge-cover(회색 가림막)
	sb.WriteString(fmt.Sprintf(
		`<div class="rom-card" data-sys="%s" data-rom="%s" onclick="Launcher.run('%s', '%s')" oncontextmenu="App.showCtx(event, '%s', '%s')" ontouchstart="App.handleTouch(event, '%s', '%s')">`+
		`<span class="rom-name">%s</span>`+
		`<div class="size-gauge"><div class="gauge-cover" style="width:%.1f%%"></div></div>`+
		`</div>`, 
		safeSys, safeRom, safeSys, safeRom, safeSys, safeRom, safeSys, safeRom, romNameDisp, coverPercent))
}

// [수정] 롬 목록 HTML 생성기
func generateRomHTML(baseDir string) string {
	var sb strings.Builder
	
	// 1. 디렉토리 스캔 및 데이터 수집
	romData := make(map[string][]RomInfo)
	entries, _ := os.ReadDir(baseDir)
	
	for _, entry := range entries {
		if entry.IsDir() {
			sysName := entry.Name()
			romEntries, _ := os.ReadDir(filepath.Join(baseDir, sysName))
			var list []RomInfo
			for _, rom := range romEntries {
				if !rom.IsDir() {
					ext := strings.ToLower(filepath.Ext(rom.Name()))
					if ext == ".zip" || ext == ".7z" || ext == ".gba" || ext == ".nds" || ext == ".iso" || ext == ".bin" || ext == ".chd" || ext == ".sfc" || ext == ".smc" {
						info, _ := rom.Info()
						list = append(list, RomInfo{Name: rom.Name(), Size: info.Size()})
					}
				}
			}
			if len(list) > 0 {
				romData[sysName] = list
			}
		}
	}

	// 2. 시스템 이름 정렬
	var systems []string
	for sys := range romData {
		systems = append(systems, sys)
	}
	sort.Strings(systems)

	// 3. HTML 조립
	for _, sys := range systems {
		roms := romData[sys]
		sort.Slice(roms, func(i, j int) bool { return roms[i].Name < roms[j].Name })

		sb.WriteString(fmt.Sprintf(`<div class="category"><div class="category-title" style="border-left-color: #E55B5B;">%s <span class="game-count">(%d)</span></div><div class="rom-grid">`, sys, len(roms)))

		for _, rom := range roms {
			writeCardHTML(&sb, sys, rom.Name, rom.Size)
		}
		sb.WriteString(`</div></div>`) // end category
	}

	if sb.Len() == 0 {
		return `<div style="text-align:center; padding:50px; color:#aaa;">게임 파일이 없습니다.<br>./data/roms 폴더에 게임을 넣어주세요.</div>`
	}

	return sb.String()
}

// Depth 1 변경 감지
func getLatestModTime(baseDir string) time.Time {
	latest := time.Time{}
	info, err := os.Stat(baseDir)
	if err != nil { return latest }
	latest = info.ModTime()

	entries, _ := os.ReadDir(baseDir)
	for _, entry := range entries {
		if entry.IsDir() {
			subInfo, err := entry.Info()
			if err == nil && subInfo.ModTime().After(latest) {
				latest = subInfo.ModTime()
			}
		}
	}
	return latest
}

// [수정] handleIndex: 파일 크기(Size)와 수정 시간(Time) 모두 체크하여 캐시 갱신
func handleIndex(w http.ResponseWriter, r *http.Request) {
	romsDir := "./data/roms"
	indexFile := "index.html"

	// 1. 변경 감지 (롬 폴더나 index.html이 바뀌었을 때만 갱신)
	currentLatestTime := getLatestModTime(romsDir)
	indexInfo, err := os.Stat(indexFile)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	currentIndexTime := indexInfo.ModTime()
	currentIndexSize := indexInfo.Size() // [추가] 파일 크기 정보

	isGzip := EnableGzip && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")

	var contentToServe []byte
	var etagToServe string

	// 2. 캐시 확인
	indexCache.RLock()
	isValid := !indexCache.RomsDirTime.IsZero() &&
		indexCache.RomsDirTime.Equal(currentLatestTime) &&
		indexCache.IndexFileTime.Equal(currentIndexTime) &&
		indexCache.IndexFileSize == currentIndexSize // [추가] 크기 비교

	if isValid {
		etagToServe = indexCache.ETag
		if isGzip {
			contentToServe = indexCache.Content
		} else {
			contentToServe = indexCache.RawContent
		}
		indexCache.RUnlock()
	} else {
		indexCache.RUnlock()
		indexCache.Lock()

		// Double-check (Write Lock 진입 후 다시 확인)
		if !indexCache.RomsDirTime.IsZero() &&
			indexCache.RomsDirTime.Equal(currentLatestTime) &&
			indexCache.IndexFileTime.Equal(currentIndexTime) &&
			indexCache.IndexFileSize == currentIndexSize {
			
			etagToServe = indexCache.ETag
			if isGzip {
				contentToServe = indexCache.Content
			} else {
				contentToServe = indexCache.RawContent
			}
			indexCache.Unlock()
		} else {
			rawHTML, err := os.ReadFile(indexFile)
			if err != nil {
				indexCache.Unlock()
				http.Error(w, "Index file error", 500)
				return
			}

			romHTML := generateRomHTML(romsDir)
			finalStr := strings.Replace(string(rawHTML), "<!-- SERVER_RENDERED_CONTENT -->", romHTML, 1)

			hash := md5.Sum([]byte(finalStr))
			newETag := hex.EncodeToString(hash[:])
			
			var gzipData []byte
			if EnableGzip {
				gzipData = compressGzip([]byte(finalStr))
			}

			indexCache.RawContent = []byte(finalStr)
			if EnableGzip {
				indexCache.Content = gzipData
			}
			indexCache.RomsDirTime = currentLatestTime
			indexCache.IndexFileTime = currentIndexTime
			indexCache.IndexFileSize = currentIndexSize // [추가] 크기 저장
			indexCache.ETag = newETag

			etagToServe = newETag
			if isGzip {
				contentToServe = gzipData
			} else {
				contentToServe = []byte(finalStr)
			}
			indexCache.Unlock()
		}
	}

	if match := r.Header.Get("If-None-Match"); match != "" {
		if strings.Contains(match, etagToServe) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", fmt.Sprintf(`"%s"`, etagToServe))
	w.Header().Set("Cache-Control", "no-cache")
	if isGzip {
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Write(contentToServe)
}

func handleBookmark(w http.ResponseWriter, r *http.Request) {
	filePath := "./data/bookmark.json"

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		os.WriteFile(filePath, []byte("[]"), 0644)
	}

	bytesData, _ := os.ReadFile(filePath)
	var bookmarks []BookmarkItem
	json.Unmarshal(bytesData, &bookmarks)

	switch r.Method {
	case "GET":
		if r.URL.Query().Get("format") == "html" {
			grouped := make(map[string][]RomInfo)
			for _, item := range bookmarks {
				romPath := filepath.Join("./data/roms", item.System, item.Rom)
				var size int64 = 0
				if info, err := os.Stat(romPath); err == nil {
					size = info.Size()
				}
				grouped[item.System] = append(grouped[item.System], RomInfo{Name: item.Rom, Size: size})
			}

			var systems []string
			for sys := range grouped {
				systems = append(systems, sys)
			}
			sort.Strings(systems)

			var sb strings.Builder
			if len(systems) == 0 {
				sb.WriteString(`<div style="text-align:center; padding:50px; color:#aaa;">즐겨찾기된 게임이 없습니다.</div>`)
			} else {
				for _, sys := range systems {
					roms := grouped[sys]
					sort.Slice(roms, func(i, j int) bool { return roms[i].Name < roms[j].Name })

					sb.WriteString(fmt.Sprintf(`<div class="category"><div class="category-title" style="border-left-color: #F57C00;">%s <span class="game-count">(%d)</span></div><div class="rom-grid">`, sys, len(roms)))
					
					for _, rom := range roms {
						writeCardHTML(&sb, sys, rom.Name, rom.Size)
					}
					sb.WriteString(`</div></div>`)
				}
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(sb.String()))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(bytesData)

	case "POST":
		var item BookmarkItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "Invalid JSON", 400)
			return
		}
		for _, b := range bookmarks {
			if b.System == item.System && b.Rom == item.Rom {
				w.WriteHeader(200)
				return
			}
		}
		bookmarks = append(bookmarks, item)
		saveBookmarks(filePath, bookmarks)
		w.WriteHeader(200)

	case "DELETE":
		var item BookmarkItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "Invalid JSON", 400)
			return
		}
		newBookmarks := []BookmarkItem{}
		for _, b := range bookmarks {
			if !(b.System == item.System && b.Rom == item.Rom) {
				newBookmarks = append(newBookmarks, b)
			}
		}
		saveBookmarks(filePath, newBookmarks)
		w.WriteHeader(200)
	}
}

// ... (나머지 handle functions: saveBookmarks, handleRomDelete, handleSaveUpload, handleSaveDownload, handleCoreDownload, handleInjectRom 등 유지) ...

func saveBookmarks(path string, data []BookmarkItem) {
	bytesData, _ := json.MarshalIndent(data, "", "  ")
	os.WriteFile(path, bytesData, 0644)
}

func handleRomDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "DELETE" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	sys := r.URL.Query().Get("sys")
	rom := r.URL.Query().Get("rom")
	if sys == "" || rom == "" {
		http.Error(w, "Missing params", 400)
		return
	}
	safeSys := filepath.Base(sys)
	safeRom := filepath.Base(rom)
	targetPath := filepath.Join("./data/roms", safeSys, safeRom)
	if err := os.Remove(targetPath); err != nil {
		log.Printf("삭제 실패: %v", err)
		http.Error(w, "Delete failed", 500)
		return
	}
	w.WriteHeader(200)
}

func handleSaveUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "Missing name", 400)
		return
	}
	safeName := filepath.Base(name)
	saveDir := "./data/saves"
	os.MkdirAll(saveDir, 0755)
	targetPath := filepath.Join(saveDir, safeName)
	file, err := os.Create(targetPath)
	if err != nil {
		http.Error(w, "Create failed", 500)
		return
	}
	defer file.Close()
	if _, err := io.Copy(file, r.Body); err != nil {
		http.Error(w, "Write failed", 500)
		return
	}
	w.WriteHeader(200)
}

func handleSaveDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "Missing name", 400)
		return
	}
	safeName := filepath.Base(name)
	targetPath := filepath.Join("./data/saves", safeName)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		http.Error(w, "Not found", 404)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", safeName))
	http.ServeFile(w, r, targetPath)
}

func handleCoreDownload(w http.ResponseWriter, r *http.Request) {
	processingMutex.Lock()
	if processingFiles["core_download"] {
		processingMutex.Unlock()
		http.Error(w, "이미 다운로드가 진행 중입니다.", 429)
		return
	}
	processingFiles["core_download"] = true
	processingMutex.Unlock()

	defer func() {
		processingMutex.Lock()
		delete(processingFiles, "core_download")
		processingMutex.Unlock()
	}()

	syncInfoPath := "./data/core_sync.json"
	var localSync SyncInfo

	if data, err := os.ReadFile(syncInfoPath); err == nil {
		json.Unmarshal(data, &localSync)
		if localSync.LastSyncTime > 0 {
			elapsed := time.Now().Unix() - localSync.LastSyncTime
			cooldown := int64(24 * 60 * 60)
			if elapsed < cooldown {
				remaining := cooldown - elapsed
				hours := remaining / 3600
				minutes := (remaining % 3600) / 60
				msg := fmt.Sprintf("코어 동기화는 24시간마다 가능합니다.\n남은 시간: %d시간 %d분", hours, minutes)
				http.Error(w, msg, 429)
				return
			}
		}
	}

	cdnBaseUrl := "https://cdn.emulatorjs.org/latest/"
	localBaseDir := "./emulatorjs/"
	tmpBaseDir := "/tmp/cv/cores"
	os.RemoveAll(tmpBaseDir)
	if err := os.MkdirAll(tmpBaseDir, 0755); err != nil {
		log.Printf("[Sync] 임시 디렉토리 생성 실패: %v", err)
	}

	filesToSync := []string{
		"build.js", "index.html", "package-lock.json", "package.json", "update.js",
		"data/emulator.css", "data/emulator.min.css", "data/emulator.min.zip",
		"data/loader.js", "data/version.json", "data/compression/extract7z.js",
		"data/compression/extractzip.js", "data/compression/libunrar.js", "data/compression/libunrar.wasm",
		"data/localization/ar.json", "data/localization/bn.json", "data/localization/de.json",
		"data/localization/el.json", "data/localization/en.json", "data/localization/es.json",
		"data/localization/fa.json", "data/localization/fr.json", "data/localization/hi.json",
		"data/localization/it.json", "data/localization/ja.json", "data/localization/jv.json",
		"data/localization/ko.json", "data/localization/pt.json", "data/localization/retroarch.json",
		"data/localization/ro.json", "data/localization/ru.json", "data/localization/tr.json",
		"data/localization/vi.json", "data/localization/zh.json",
		"data/src/compression.js", "data/src/emulator.js", "data/src/GameManager.js",
		"data/src/gamepad.js", "data/src/nipplejs.js", "data/src/shaders.js",
		"data/src/socket.io.min.js", "data/src/storage.js", "minify/minify.js",
	}

	coreFiles := []string{
		"fbneo-wasm.data", "fbneo-thread-wasm.data", "fbneo-legacy-wasm.data",
		"mame2003_plus-wasm.data", "mame2003_plus-thread-wasm.data", "mame2003_plus-legacy-wasm.data",
		"mgba-wasm.data", "mgba-thread-wasm.data", "mgba-legacy-wasm.data",
		"melonds-wasm.data", "melonds-thread-wasm.data", "melonds-legacy-wasm.data",
		"mednafen_psx_hw-wasm.data", "mednafen_psx_hw-thread-wasm.data", "mednafen_psx_hw-legacy-wasm.data",
	}

	log.Println("[Core] 에뮬레이터 데이터 동기화 시작...")
	client := http.Client{Timeout: 300 * time.Second}
	successCount := 0
	totalFiles := len(filesToSync) + len(coreFiles)

	downloadToPath := func(url, destPath string) bool {
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return false
		}
		resp, err := client.Get(url)
		if err != nil || resp.StatusCode != 200 {
			return false
		}
		defer resp.Body.Close()
		out, err := os.Create(destPath)
		if err != nil {
			return false
		}
		defer out.Close()
		_, err = io.Copy(out, resp.Body)
		return err == nil
	}

	for _, file := range filesToSync {
		if downloadToPath(cdnBaseUrl+file, filepath.Join(localBaseDir, file)) {
			successCount++
		}
	}

	for _, coreFile := range coreFiles {
		url := cdnBaseUrl + "data/cores/" + coreFile
		tmpDownPath := filepath.Join(tmpBaseDir, coreFile)
		if !downloadToPath(url, tmpDownPath) {
			continue
		}
		extractDir := tmpDownPath + "_ext"
		os.MkdirAll(extractDir, 0755)
		exec.Command("7z", "x", tmpDownPath, "-o"+extractDir, "-y").Run()
		targetPath := filepath.Join(localBaseDir, "data/cores", coreFile)
		if err := zipDirToFile(extractDir, targetPath); err == nil {
			successCount++
		}
	}

	localSync.LastSyncTime = time.Now().Unix()
	if bytesData, err := json.Marshal(localSync); err == nil {
		os.MkdirAll("./data", 0755)
		os.WriteFile(syncInfoPath, bytesData, 0644)
	}
	os.RemoveAll(tmpBaseDir)

	w.WriteHeader(200)
	fmt.Fprintf(w, "업데이트 완료: 총 %d개 파일 중 %d개 성공. [TIMESTAMP:%d]", totalFiles, successCount, localSync.LastSyncTime)
}

func loadInjectLog() InjectLog {
	filePath := "./data/injected.json"
	data, err := os.ReadFile(filePath)
	if err != nil {
		return make(InjectLog)
	}
	var log InjectLog
	json.Unmarshal(data, &log)
	return log
}

func saveInjectLog(log InjectLog) {
	data, _ := json.MarshalIndent(log, "", "  ")
	os.WriteFile("./data/injected.json", data, 0644)
}

func handleInjectRom(w http.ResponseWriter, r *http.Request) {
	sys := r.URL.Query().Get("sys")
	rom := r.URL.Query().Get("rom")
	injectParam := r.URL.Query().Get("inject")

	if sys == "" || rom == "" || injectParam == "" {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	lockKey := fmt.Sprintf("inject:%s:%s", sys, rom)
	processingMutex.Lock()
	if processingFiles[lockKey] {
		processingMutex.Unlock()
		http.Error(w, "이미 작업 중입니다.", 429)
		return
	}
	processingFiles[lockKey] = true
	processingMutex.Unlock()

	defer func() {
		processingMutex.Lock()
		delete(processingFiles, lockKey)
		processingMutex.Unlock()
	}()

	injectList := strings.Split(injectParam, ",")
	sort.Strings(injectList)
	injectKey := strings.Join(injectList, ",")
	romKey := fmt.Sprintf("%s/%s", sys, rom)

	injectLog := loadInjectLog()
	if savedInject, ok := injectLog[romKey]; ok && savedInject == injectKey {
		w.WriteHeader(200)
		fmt.Fprint(w, "Already injected")
		return
	}

	safeSys := filepath.Base(sys)
	safeRom := filepath.Base(rom)
	romPath := filepath.Join("./data/roms", safeSys, safeRom)
	romNameNoExt := strings.TrimSuffix(safeRom, filepath.Ext(safeRom))
	workDir := filepath.Join("/tmp", "cv", romNameNoExt)

	defer os.RemoveAll(workDir)

	if err := unzipToDir(romPath, workDir); err != nil {
		http.Error(w, "Unzip failed", 500)
		return
	}

	for _, injectFile := range injectList {
		cleanPath := filepath.ToSlash(filepath.Clean(injectFile))
		if strings.HasPrefix(cleanPath, "/") {
			cleanPath = "." + cleanPath
		} else if !strings.HasPrefix(cleanPath, ".") {
			cleanPath = "./" + cleanPath
		}
		if _, err := os.Stat(cleanPath); os.IsNotExist(err) {
			continue
		}
		if strings.HasSuffix(strings.ToLower(cleanPath), ".zip") {
			unzipToDir(cleanPath, workDir)
		} else {
			copyFile(cleanPath, filepath.Join(workDir, filepath.Base(cleanPath)))
		}
	}

	zipTempPath := filepath.Join("/tmp", "cv", "temp_"+safeRom)
	os.MkdirAll(filepath.Dir(zipTempPath), 0755)
	if err := zipDirToFile(workDir, zipTempPath); err != nil {
		http.Error(w, "Re-zip failed", 500)
		return
	}
	defer os.Remove(zipTempPath)

	if err := moveFile(zipTempPath, romPath); err != nil {
		http.Error(w, "Overwrite failed", 500)
		return
	}

	injectLog[romKey] = injectKey
	saveInjectLog(injectLog)

	w.WriteHeader(200)
	fmt.Fprint(w, "Injection complete")
}

func unzipToDir(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}
		os.MkdirAll(filepath.Dir(fpath), os.ModePerm)
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}
		io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
	}
	return nil
}

func zipDirToFile(srcDir, destZip string) error {
	zipFile, err := os.Create(destZip)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)
		header.Method = zip.Deflate
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		io.Copy(writer, file)
		return err
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	io.Copy(out, in)
	return err
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

func addHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		if strings.Contains(r.URL.Path, "/emulatorjs/") ||
			strings.Contains(r.URL.Path, "/data/cores/") ||
			strings.HasSuffix(r.URL.Path, ".zip") ||
			strings.HasSuffix(r.URL.Path, ".7z") {
			w.Header().Set("Cache-Control", "public, max-age=31536000")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}

func wrapWithCacheHandler(fs http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			handleIndex(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	}
}

// Gzip Helper
func compressGzip(data []byte) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(data)
	w.Close()
	return b.Bytes()
}

func main() {
	fs := http.FileServer(http.Dir("."))
	http.Handle("/", addHeaders(wrapWithCacheHandler(fs)))

	http.HandleFunc("/api/disk", handleDiskInfo)
	http.HandleFunc("/api/bookmark", handleBookmark)
	http.HandleFunc("/api/rom", handleRomDelete)
	http.HandleFunc("/api/save", handleSaveUpload)
	http.HandleFunc("/api/load", handleSaveDownload)
	http.HandleFunc("/api/download-cores", handleCoreDownload)
	http.HandleFunc("/api/rom/inject", handleInjectRom)

	fmt.Println("Server started at :8080 (SSR Enabled + Optimized)")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}