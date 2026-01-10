🕹️ Retro Arcade Server (Web-based Emulator Host)

EmulatorJS 를 이용하여 라즈베리파이에 웹 게임서버를 구축합니다.
************************************************************

설치법
1. EmulatorJS 를 다운받고 적당한 서버용 폴더를 만들고 하위에 /emulatorjs 폴더에 압축을 풉니다.
2. 첨부된 emulator.min.js 를 ./emulatorjs/data/ 안에 overwrite 합니다.
    - 수정사항 : 로컬서버의 파일만 서빙합니다.
    - extractzip.js 대신 fflate.min.js 를 사용하므로 iPad 등에서 초고속 압축 해제가 가능합니다.
    - fflate 는 https://cdn.jsdelivr.net/npm/fflate@0.8.2/umd/index.min.js 을 받아서 \emulatorjs\data\compression\fflate.min.js 로 저장하세요
3. 게임은 편의적으로 폴더 mame, nds, snes, gba, neogeo 만 지원합니다. 다른 이름의 폴더의 롬은 fbneo가 실행 합니다.
4. 이제 서버를 실행하여 :8080 포트로 접속되는지 확인합니다.
5. 서버에서 "코어 동기화"를 누르면 자동으로 필수파일을 받습니다만 작동하지 않으면 ./emulatorjs 이하에 빠진 파일을 넣으세요.
6. 필수는 아니지만 아이패드 등에서 게임 시작이 과도하게 느릴경우 https 서버인 caddy 를 설치할 필요가 있습니다. 아이패드의 보안 때문입니다.
7. 코어는 코어 다운로드후 zip 으로 자동 재압축 합니다. 7z 파일은 아이패드에서 10배이상 느립니다.
   
**************************************************************

- 이하 챗지피티의 자동 소개글 -

Retro Arcade Server는 Go 언어로 작성된 경량화된 웹 에뮬레이터 호스팅 서버입니다.

EmulatorJS를 기반으로 작동하며, 로컬 네트워크나 개인 서버에서 자신의 합법적인 게임 백업(ROM)을 브라우저를 통해 플레이할 수 있도록 돕는 개인용 아카이빙 도구입니다.

이 프로젝트는 단순한 파일 서빙을 넘어, 에뮬레이터 코어의 자동 동기화, 세이브 파일의 서버 저장, BIOS/패치 파일의 실시간 병합(Injection) 기능을 제공합니다.

⚠️ Legal Disclaimer (법적 고지 및 저작권 안내)

이 프로젝트는 게임 ROM, BIOS, 또는 저작권이 있는 상용 소프트웨어를 포함하지 않습니다.

저작권 준수: 사용자는 자신이 소유한 게임의 백업(ROM) 파일과 기기에서 추출한 BIOS 파일만을 사용해야 합니다. 타인의 저작물을 무단으로 배포하거나 다운로드하는 행위는 법적 처벌을 받을 수 있습니다.

EmulatorJS 라이선스: 이 프로젝트는 EmulatorJS의 웹 인터페이스를 활용합니다. 해당 프로젝트의 라이선스(GPLv3 등)를 준수합니다.

책임의 한계: 개발자는 이 소프트웨어를 사용하여 발생하는 저작권 분쟁, 데이터 손실, 또는 기타 법적 문제에 대해 어떠한 책임도 지지 않습니다.

✨ 주요 기능 (Key Features)

⚡ 고성능 Go 서버: 가볍고 빠른 Go 언어 기반의 백엔드 서버.

📥 스마트 코어 동기화 (Smart Core Sync):

EmulatorJS 공식 CDN에서 최신 코어 및 필수 에뮬레이터 파일들을 자동으로 다운로드합니다.

.data (7z) 파일을 다운로드 후 자동으로 압축 해제 및 최적화된 포맷(Zip)으로 재압축하여 저장합니다.

CDN 남용 방지를 위해 24시간 쿨다운(Cooldown) 시스템이 적용되어 있습니다.

☁️ 세이브 데이터 클라우드 동기화:

게임 내 세이브(srm) 및 상태 저장(state) 파일을 서버에 자동으로 업로드/다운로드합니다.

기기가 바뀌어도 이어서 플레이가 가능합니다.

💉 실시간 롬 인젝트 (On-the-fly Injection):

게임 실행 시 필요한 BIOS 파일이나 패치 파일을 원본 ROM과 서버단에서 자동으로 병합하여 제공합니다.

사용자가 일일이 ROM 파일을 수정할 필요가 없습니다.

📱 반응형 웹 UI: 모바일 및 데스크탑 환경에 최적화된 터치 인터페이스와 가상 게임패드 지원.

🛡️ 보안 및 최적화:

WASM(WebAssembly) 구동을 위한 최적의 HTTP 헤더(Content-Type, Cache-Control) 자동 설정.

불필요한 중복 요청 방지.

🛠️ 설치 및 실행 방법 (Installation)

필수 요구 사항 (Prerequisites)

Go (1.18 버전 이상)

7-Zip (서버 환경의 PATH에 7z 명령어가 등록되어 있어야 합니다. 코어 파일 변환에 사용됩니다.)

1. 프로젝트 실행

# 의존성 패키지 확인 (표준 라이브러리 위주라 별도 설치 불필요 가능성 높음)
go mod init svr
go mod tidy

# 서버 실행
go run svr.go


서버가 시작되면 브라우저에서 http://localhost:8080으로 접속하세요.

2. 초기 설정 (Core Sync)

서버 실행 후 웹 인터페이스 우측 상단의 [📥 코어 동기화] 버튼을 눌러 에뮬레이터 구동에 필요한 필수 파일들을 다운로드하세요. (최초 1회 필수, 이후 24시간마다 갱신 가능)

📂 디렉토리 구조 (Directory Structure)

서버 실행 시 자동으로 필요한 폴더가 생성되지만, 게임 파일은 사용자가 직접 넣어야 합니다.

.
├── svr.go                # 메인 서버 코드
├── index.html            # 웹 인터페이스 (Frontend)
├── data/
│   ├── roms/             # [사용자] 게임 ROM 파일 위치
│   │   ├── nes/          # (예: data/roms/nes/mario.zip)
│   │   ├── snes/
│   │   └── ...
│   ├── saves/            # [자동] 세이브 파일 저장소
│   └── cores/            # [자동] 다운로드된 코어 데이터
└── emulatorjs/           # [자동] 에뮬레이터 넣는곳


참고: data/roms 폴더 내에 시스템 이름(예: snes, gba)으로 폴더를 만들고 ROM 파일을 넣으면 서버가 자동으로 인식합니다. 시스템 이름은 index.html 내 coreMap 설정과 일치해야 합니다.

⚙️ 설정 (Configuration)

시스템 및 코어 매핑

index.html 파일 내의 coreMap 객체를 수정하여 지원할 게임 시스템과 코어를 연결할 수 있습니다.

const coreMap = { 
    neogeo: "fbneo",
    fbneo:  "fbneo", 
    mame:   "mame2003_plus", 
    snes:   "snes9x", 
    gba:    "mgba", 
    // ... 추가 가능
};


BIOS 인젝트 설정

index.html 내의 INJECT 상수를 통해 시스템별로 필요한 BIOS 파일을 지정할 수 있습니다.

const INJECT = {
    neogeo: [
        "/data/bios/neogeo_small.zip" // data/roms/neogeo 폴더 내의 파일과 병합됨
    ]
};


🤝 Contributing

버그 제보 및 기능 개선 요청은 Issue를 통해 환영합니다. 단, ROM 파일 공유 요청이나 불법적인 기능 추가 요청은 즉시 차단됩니다.

📝 License

Server Code (svr.go): MIT License

Frontend (index.html): MIT License

EmulatorJS: GPLv3 License (이 프로젝트는 EmulatorJS를 포함하지 않고 다운로드하여 사용합니다.)
