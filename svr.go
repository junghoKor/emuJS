package main

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec" // 7z 명령어 실행을 위해 추가
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 설정 정보를 담을 구조체
type Config struct {
	Systems map[string]string // coreMap
}

// 동기화 시간 정보를 담을 구조체
type SyncInfo struct {
	LastSyncTime int64 `json:"lastSyncTime"`
}

// 즐겨찾기 아이템 구조체
type BookmarkItem struct {
	System string `json:"system"`
	Rom    string `json:"rom"`
}

// 롬 파일 정보를 담을 구조체
type RomInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// [신규] 인젝트 기록 관리 구조체 (이미 패치된 롬 추적)
// Key: "sys/rom.zip", Value: "inject_path1,inject_path2" (정렬된 문자열)
type InjectLog map[string]string

// [신규] 동시성 제어를 위한 변수 (중복 실행 방지)
var (
	processingMutex sync.Mutex
	processingFiles = make(map[string]bool)
)

// index.html에서 설정을 읽어옴
func loadConfigFromHTML() Config {
	config := Config{
		Systems: make(map[string]string),
	}
	
	file, err := os.Open("index.html")
	if err != nil { 
		log.Println("[Config] index.html 파일을 찾을 수 없습니다.")
		return config 
	}
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
			if len(matches) == 3 {
				config.Systems[matches[1]] = matches[2]
			}
		}
	}
	return config
}

// 디스크 정보 반환 함수
func getDiskUsage(path string) (uint64, uint64) {
	var stat syscall.Statfs_t
	syscall.Statfs(path, &stat)
	free := stat.Bavail * uint64(stat.Bsize)
	total := stat.Blocks * uint64(stat.Bsize)
	return free, total
}

// [API] 디스크 정보 핸들러
func handleDiskInfo(w http.ResponseWriter, r *http.Request) {
	wd, _ := os.Getwd()
	free, total := getDiskUsage(wd)
	
	json.NewEncoder(w).Encode(map[string]uint64{
		"free": free,
		"total": total,
	})
}

// [API] 롬 목록 핸들러
func handleRomList(w http.ResponseWriter, r *http.Request) {
	result := make(map[string][]RomInfo)
	baseDir := "./data/roms"

	entries, err := os.ReadDir(baseDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				sysName := entry.Name()
				romsDir := filepath.Join(baseDir, sysName)
				romEntries, err := os.ReadDir(romsDir)
				if err == nil {
					var romList []RomInfo
					for _, rom := range romEntries {
						if !rom.IsDir() {
							ext := strings.ToLower(filepath.Ext(rom.Name()))
							if ext == ".zip" || ext == ".7z" || ext == ".gba"|| ext == ".nds" || ext == ".iso" || ext == ".bin" || ext == ".chd" || ext == ".sfc"|| ext == ".smc"{
								info, _ := rom.Info()
								romList = append(romList, RomInfo{
									Name: rom.Name(),
									Size: info.Size(),
								})
							}
						}
					}
					if len(romList) > 0 {
						result[sysName] = romList
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// [API] 즐겨찾기 조회/추가/삭제
func handleBookmark(w http.ResponseWriter, r *http.Request) {
	filePath := "./data/bookmark.json"
	
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		os.WriteFile(filePath, []byte("[]"), 0644)
	}

	bytes, _ := os.ReadFile(filePath)
	var bookmarks []BookmarkItem
	json.Unmarshal(bytes, &bookmarks)

	switch r.Method {
	case "GET":
		w.Header().Set("Content-Type", "application/json")
		w.Write(bytes)

	case "POST":
		var item BookmarkItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "Invalid JSON", 400); return
		}
		for _, b := range bookmarks {
			if b.System == item.System && b.Rom == item.Rom {
				w.WriteHeader(200); return
			}
		}
		bookmarks = append(bookmarks, item)
		saveBookmarks(filePath, bookmarks)
		w.WriteHeader(200)

	case "DELETE":
		var item BookmarkItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			http.Error(w, "Invalid JSON", 400); return
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

func saveBookmarks(path string, data []BookmarkItem) {
	bytes, _ := json.MarshalIndent(data, "", "  ")
	os.WriteFile(path, bytes, 0644)
}

// [API] 롬 삭제
func handleRomDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "DELETE" { http.Error(w, "Method not allowed", 405); return }
	
	sys := r.URL.Query().Get("sys")
	rom := r.URL.Query().Get("rom")
	
	if sys == "" || rom == "" { http.Error(w, "Missing params", 400); return }

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

// [API] 세이브 데이터 업로드
func handleSaveUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" { http.Error(w, "Method not allowed", 405); return }
	
	name := r.URL.Query().Get("name")
	if name == "" { http.Error(w, "Missing name", 400); return }
	
	safeName := filepath.Base(name)
	saveDir := "./data/saves"
	os.MkdirAll(saveDir, 0755)
	
	targetPath := filepath.Join(saveDir, safeName)
	
	file, err := os.Create(targetPath)
	if err != nil { http.Error(w, "Create failed", 500); return }
	defer file.Close()
	
	if _, err := io.Copy(file, r.Body); err != nil {
		http.Error(w, "Write failed", 500)
		return
	}
	w.WriteHeader(200)
}

// [API] 세이브 데이터 다운로드
func handleSaveDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" { http.Error(w, "Method not allowed", 405); return }

	name := r.URL.Query().Get("name")
	if name == "" { http.Error(w, "Missing name", 400); return }

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

// [API] 코어 및 에뮬레이터 필수 파일 다운로드 핸들러 (24시간 쿨다운 적용됨)
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

	// 1. 쿨다운 체크 (24시간)
	syncInfoPath := "./data/core_sync.json"
	var localSync SyncInfo
	
	// 파일이 있으면 읽어서 시간 확인
	if data, err := os.ReadFile(syncInfoPath); err == nil {
		json.Unmarshal(data, &localSync)
		
		if localSync.LastSyncTime > 0 {
			elapsed := time.Now().Unix() - localSync.LastSyncTime
			cooldown := int64(24 * 60 * 60) // 24시간
			
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

	// 설정 정의
	cdnBaseUrl := "https://cdn.emulatorjs.org/latest/"
	localBaseDir := "./emulatorjs/"
	
	// 임시 다운로드 경로 정의
	tmpBaseDir := "/tmp/cv/cores"
	os.RemoveAll(tmpBaseDir) // 이전 작업 잔여물 제거
	if err := os.MkdirAll(tmpBaseDir, 0755); err != nil {
		log.Printf("[Sync] 임시 디렉토리 생성 실패: %v", err)
	}

	// 1. 다운로드할 필수 정적 파일 목록 (일반 다운로드)
	filesToSync := []string{
		"build.js",
		"index.html",
		"package-lock.json",
		"package.json",
		"update.js",
		"data/emulator.css",
		"data/emulator.min.css",
		//"data/emulator.min.js", // 사용자 요청 주석 유지
		"data/emulator.min.zip",
		"data/loader.js",
		"data/version.json",
		"data/compression/extract7z.js",
		"data/compression/extractzip.js",
		"data/compression/libunrar.js",
		"data/compression/libunrar.wasm",
		// Localizations
		"data/localization/ar.json", "data/localization/bn.json", "data/localization/de.json",
		"data/localization/el.json", "data/localization/en.json", "data/localization/es.json",
		"data/localization/fa.json", "data/localization/fr.json", "data/localization/hi.json",
		"data/localization/it.json", "data/localization/ja.json", "data/localization/jv.json",
		"data/localization/ko.json", "data/localization/pt.json", "data/localization/retroarch.json",
		"data/localization/ro.json", "data/localization/ru.json", "data/localization/tr.json",
		"data/localization/vi.json", "data/localization/zh.json",
		// Sources
		"data/src/compression.js", "data/src/emulator.js", "data/src/GameManager.js",
		"data/src/gamepad.js", "data/src/nipplejs.js", "data/src/shaders.js",
		"data/src/socket.io.min.js", "data/src/storage.js",
		"minify/minify.js",
	}

	// 2. 다운로드할 코어 데이터 파일 목록 (7z -> Zip 변환 필요)
	coreFiles := []string{
		"fbneo-wasm.data",
		"fbneo-thread-wasm.data",
		"fbneo-legacy-wasm.data",
		"mame2003_plus-wasm.data",
		"mame2003_plus-thread-wasm.data",
		"mame2003_plus-legacy-wasm.data",
		"mgba-wasm.data",
		"mgba-thread-wasm.data",
		"mgba-legacy-wasm.data",
		"melonds-wasm.data",
		"melonds-thread-wasm.data",
		"melonds-legacy-wasm.data",
		"mednafen_psx_hw-wasm.data",
		"mednafen_psx_hw-thread-wasm.data",
		"mednafen_psx_hw-legacy-wasm.data",
	}

	log.Println("[Core] 에뮬레이터 데이터 동기화 시작...")
	
	client := http.Client{Timeout: 300 * time.Second} // 타임아웃 넉넉하게
	successCount := 0
	totalFiles := len(filesToSync) + len(coreFiles)

	// [Helper] 파일 다운로드 함수
	downloadToPath := func(url, destPath string) bool {
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			log.Printf("[Sync] 디렉토리 생성 실패: %v", err)
			return false
		}

		resp, err := client.Get(url)
		if err != nil {
			log.Printf("[Sync] 다운로드 실패 (%s): %v", url, err)
			return false
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Printf("[Sync] 상태 코드 오류 (%s): %d", url, resp.StatusCode)
			return false
		}

		out, err := os.Create(destPath)
		if err != nil {
			log.Printf("[Sync] 파일 생성 실패: %v", err)
			return false
		}
		defer out.Close()

		_, err = io.Copy(out, resp.Body)
		return err == nil
	}

	// 3. 기본 파일 동기화
	for _, file := range filesToSync {
		url := cdnBaseUrl + file
		destPath := filepath.Join(localBaseDir, file)
		
		if downloadToPath(url, destPath) {
			successCount++
		}
	}

	// 4. 코어 파일 동기화 (7z -> Zip 변환)
	for _, coreFile := range coreFiles {
		// 원본 URL (7z)
		url := cdnBaseUrl + "data/cores/" + coreFile
		
		// 임시 다운로드 경로 (/tmp/cv/cores/filename.data)
		tmpDownPath := filepath.Join(tmpBaseDir, coreFile)
		
		log.Printf("[Sync] 다운로드 중 (7z): %s", coreFile)
		if !downloadToPath(url, tmpDownPath) {
			continue
		}

		// 압축 해제용 폴더 생성 (/tmp/cv/cores/filename.data_ext)
		extractDir := tmpDownPath + "_ext"
		if err := os.MkdirAll(extractDir, 0755); err != nil {
			log.Printf("[Sync] 임시 압축해제 폴더 생성 실패: %v", err)
			continue
		}

		// 7z 압축 해제 (시스템 명령어 사용)
		// -y: 모든 질문에 yes, -o: 출력 디렉토리
		cmd := exec.Command("7z", "x", tmpDownPath, "-o"+extractDir, "-y")
		if output, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[Sync] 7z 압축 해제 실패 (%s): %v\n%s", coreFile, err, string(output))
			continue
		}

		// 최종 목적지 경로 (./emulatorjs/data/cores/filename.data) - 이름은 같지만 내용은 Zip
		targetPath := filepath.Join(localBaseDir, "data/cores", coreFile)
		
		// Zip으로 재압축
		if err := zipDirToFile(extractDir, targetPath); err != nil {
			log.Printf("[Sync] Zip 재압축 실패 (%s): %v", coreFile, err)
			continue
		}

		log.Printf("[Sync] 변환 완료 (7z -> Zip): %s", coreFile)
		successCount++
	}

	// 동기화 시간 업데이트 및 저장
	localSync.LastSyncTime = time.Now().Unix()
	if bytes, err := json.Marshal(localSync); err == nil {
		os.MkdirAll("./data", 0755)
		os.WriteFile(syncInfoPath, bytes, 0644)
	}
	
	// 임시 폴더 정리
	os.RemoveAll(tmpBaseDir)

	w.WriteHeader(200)
	fmt.Fprintf(w, "업데이트 완료: 총 %d개 파일 중 %d개 성공. [TIMESTAMP:%d]", totalFiles, successCount, localSync.LastSyncTime)
}

// [신규] 인젝트 기록 관리 함수
func loadInjectLog() InjectLog {
	filePath := "./data/injected.json"
	data, err := os.ReadFile(filePath)
	if err != nil { return make(InjectLog) }
	
	var log InjectLog
	if err := json.Unmarshal(data, &log); err != nil { return make(InjectLog) }
	return log
}

func saveInjectLog(log InjectLog) {
	data, _ := json.MarshalIndent(log, "", "  ")
	os.WriteFile("./data/injected.json", data, 0644)
}

// [API] 롬 인젝트 핸들러 (서버 사이드 파일 병합)
func handleInjectRom(w http.ResponseWriter, r *http.Request) {
	sys := r.URL.Query().Get("sys")
	rom := r.URL.Query().Get("rom")
	injectParam := r.URL.Query().Get("inject")

	if sys == "" || rom == "" || injectParam == "" {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	// 동시성 제어 (같은 파일에 대해 동시에 작업하지 않도록)
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

	// 1. 인젝트 필요 여부 확인
	injectList := strings.Split(injectParam, ",")
	sort.Strings(injectList) // 정렬하여 비교
	injectKey := strings.Join(injectList, ",")
	romKey := fmt.Sprintf("%s/%s", sys, rom)

	injectLog := loadInjectLog()
	if savedInject, ok := injectLog[romKey]; ok {
		if savedInject == injectKey {
			log.Printf("[Inject] 이미 처리된 롬입니다: %s (Skip)", romKey)
			w.WriteHeader(200)
			fmt.Fprint(w, "Already injected")
			return
		}
	}

	// 2. 경로 설정
	safeSys := filepath.Base(sys)
	safeRom := filepath.Base(rom)
	romPath := filepath.Join("./data/roms", safeSys, safeRom)
	
	// 임시 작업 폴더: /tmp/cv/{rom_name_noext} (램 디스크 활용)
	romNameNoExt := strings.TrimSuffix(safeRom, filepath.Ext(safeRom))
	workDir := filepath.Join("/tmp", "cv", romNameNoExt)

	defer os.RemoveAll(workDir)

	// 3. 작업 시작: 원본 ROM 압축 해제
	log.Printf("[Inject] 작업 시작: %s -> %s", romKey, workDir)
	
	if err := unzipToDir(romPath, workDir); err != nil {
		log.Printf("[Inject] 원본 압축 해제 실패: %v", err)
		http.Error(w, "Unzip failed", 500)
		return
	}

	// 4. 인젝트 파일들 압축 해제 또는 복사 (덮어쓰기)
	for _, injectFile := range injectList {
		// [수정] 경로 보정: /data/bios/... -> ./data/bios/...
		cleanPath := filepath.ToSlash(filepath.Clean(injectFile))
		if strings.HasPrefix(cleanPath, "/") {
			cleanPath = "." + cleanPath
		} else if !strings.HasPrefix(cleanPath, ".") {
			cleanPath = "./" + cleanPath
		}

		if _, err := os.Stat(cleanPath); os.IsNotExist(err) {
			log.Printf("[Inject] ⚠️ 주입 파일 없음: %s (원본 경로: %s)", cleanPath, injectFile)
			continue
		}

		ext := strings.ToLower(filepath.Ext(cleanPath))
		if ext == ".zip" {
			// ZIP이면 압축 해제하여 병합
			log.Printf("[Inject] ZIP 병합: %s", cleanPath)
			if err := unzipToDir(cleanPath, workDir); err != nil {
				log.Printf("[Inject] 주입 파일 압축 해제 실패 (%s): %v", cleanPath, err)
			}
		} else {
			// 일반 파일이면 복사
			log.Printf("[Inject] 파일 추가: %s", cleanPath)
			dstPath := filepath.Join(workDir, filepath.Base(cleanPath))
			if err := copyFile(cleanPath, dstPath); err != nil {
				log.Printf("[Inject] 파일 복사 실패 (%s): %v", cleanPath, err)
			}
		}
	}

	// 5. 다시 압축하여 임시 파일 생성 (/tmp/cv/ 경로 사용)
	zipTempPath := filepath.Join("/tmp", "cv", "temp_"+safeRom)
	os.MkdirAll(filepath.Dir(zipTempPath), 0755)
	
	if err := zipDirToFile(workDir, zipTempPath); err != nil {
		log.Printf("[Inject] 재압축 실패: %v", err)
		http.Error(w, "Re-zip failed", 500)
		return
	}
	defer os.Remove(zipTempPath) 

	// 6. 원본 파일 덮어쓰기
	if err := moveFile(zipTempPath, romPath); err != nil {
		log.Printf("[Inject] 원본 파일 교체 실패: %v", err)
		http.Error(w, "Overwrite failed", 500)
		return
	}

	// 7. 기록 저장
	injectLog[romKey] = injectKey
	saveInjectLog(injectLog)

	log.Printf("[Inject] 작업 완료: %s", romKey)
	w.WriteHeader(200)
	fmt.Fprint(w, "Injection complete")
}

// ZIP 압축 해제 함수
func unzipToDir(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil { return err }
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

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		// [수정] 파일 생성 시 권한 0644 강제 (ZIP 내부 권한 무시)
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil { return err }

		rc, err := f.Open()
		if err != nil { outFile.Close(); return err }

		_, err = io.Copy(outFile, rc)

		outFile.Close()
		rc.Close()
		if err != nil { return err }
	}
	return nil
}

// 폴더를 ZIP으로 압축하는 함수
func zipDirToFile(srcDir, destZip string) error {
	zipFile, err := os.Create(destZip)
	if err != nil { return err }
	defer zipFile.Close()

	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil { return err }
		if info.IsDir() { return nil }

		header, err := zip.FileInfoHeader(info)
		if err != nil { return err }

		// ZIP 내부 경로는 상대 경로로
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil { return err }
		
		header.Name = filepath.ToSlash(relPath)
		header.Method = zip.Deflate // 일반적인 ZIP 압축률 (Deflate) 사용

		writer, err := archive.CreateHeader(header)
		if err != nil { return err }

		file, err := os.Open(path)
		if err != nil { return err }
		defer file.Close()

		_, err = io.Copy(writer, file)
		return err
	})
}

// 파일 복사 함수
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil { return err }
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil { return err }
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// 파일 이동
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil { return nil }

	if err := copyFile(src, dst); err != nil { return err }
	return os.Remove(src)
}

// [신규] 파일 서버 미들웨어 (헤더 설정용)
func addHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. WASM 파일의 MIME Type 강제 설정 (중요)
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}

		// 2. 멀티스레드 및 고해상도 타이머를 위한 헤더
		// (싱글 스레드에서도 정밀한 에뮬레이션 타이밍을 위해 설정하는 것이 권장됨)
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")

		// 3. 캐싱 설정 (코어/롬 등 대용량 파일은 1년 캐싱)
		if strings.Contains(r.URL.Path, "/emulatorjs/") || 
		   strings.Contains(r.URL.Path, "/data/cores/") || 
		   strings.HasSuffix(r.URL.Path, ".zip") || 
		   strings.HasSuffix(r.URL.Path, ".7z") {
			w.Header().Set("Cache-Control", "public, max-age=31536000") // 1년
		} else {
			// 그 외 파일은 변경 확인을 위해 짧게 설정하거나 no-cache
			w.Header().Set("Cache-Control", "no-cache") 
		}

		h.ServeHTTP(w, r)
	})
}

func main() {
	// [수정] 단순 FileServer 대신 커스텀 핸들러 사용
	fs := http.FileServer(http.Dir("."))
	http.Handle("/", addHeaders(fs))
	
	http.HandleFunc("/api/disk", handleDiskInfo)
	http.HandleFunc("/api/roms", handleRomList)
	http.HandleFunc("/api/bookmark", handleBookmark)
	http.HandleFunc("/api/rom", handleRomDelete)
	http.HandleFunc("/api/save", handleSaveUpload)
	http.HandleFunc("/api/load", handleSaveDownload)
	http.HandleFunc("/api/download-cores", handleCoreDownload)
	http.HandleFunc("/api/rom/inject", handleInjectRom) 

	fmt.Println("Server started at :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}
