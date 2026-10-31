# 부끄일기 · Buku Journal

두 사람의 기억을 짧은 문장으로 남기는 비공개 일기. Go 서버가 파일과 jj 이력을 관리하고, Codex CLI가 자연어 입력을 기존 기록의 말투에 맞춰 정리합니다. 웹 화면은 빌드 과정 없는 HTML/CSS/JavaScript로 만든 피드와 달력입니다.

기록은 소스 저장소나 DBMS에 넣지 않습니다. Markdown과 TXT 파일을 에디터에서 직접 읽고 수정할 수 있으며, 앱은 파일에서 화면을 다시 구성합니다. Linux `aarch64-linux`, `x86_64-linux`만 지원합니다.

## 실행

```sh
jj st                              # 새 파일을 Nix 소스에 포함
nix build .
nix run . -- serve                 # 기본 127.0.0.1:3002
```

브라우저에서 `http://127.0.0.1:3002`에 접속합니다. 처음 실행할 때 생성되는 접속 키로 로그인하세요.

```sh
cat "${XDG_CONFIG_HOME:-$HOME/.config}/buku-journal/token"
```

접속 키는 비밀번호처럼 보관합니다. 브라우저에 Codex 인증 정보가 전달되지 않으며, 기록 API는 로그인한 세션에서만 사용할 수 있습니다. HTTPS 외부 접속은 아래 hooddy 배포를 사용합니다.

Codex는 서버를 실행하는 사용자의 인증을 사용합니다. 같은 사용자로 `codex login`을 완료해 두세요. 개별 요청은 사용자 설정과 규칙을 읽지 않는 별도 `codex exec` 작업으로 실행됩니다. 인증 만료나 실행 실패 시 입력은 보존되며 웹에서 다시 시도할 수 있습니다.

```sh
nix develop
codex login status
codex login                         # 인증이 없거나 만료된 경우
```

기존 기록 경로를 그대로 연결하거나 별도 포트를 사용할 수도 있습니다.

```sh
nix run . -- serve \
  --journal-dir /absolute/path/to/journal \
  --listen 127.0.0.1:3002
```

그 밖의 옵션은 `nix run . -- serve --help`로 확인합니다. 시스템 서비스로 띄울 때에도 같은 사용자의 인증 경로를 유지해야 합니다.

기본 설정은 `$XDG_CONFIG_HOME/buku-journal/config.toml`(기본 `~/.config/buku-journal/config.toml`)에서도 지정합니다. 평평한 `키 = "문자열"` 형식이며 CLI 옵션이 우선합니다.

```toml
listen = "127.0.0.1:3002"
timezone = "Asia/Seoul"
worker_timeout = "5m"
# journal_dir = "/absolute/path/to/journal"
```

지원 키는 `journal_dir`, `state_dir`, `listen`, `timezone`, `codex`, `jj`, `model`, `token_file`, `worker_timeout`입니다. 별도 모델을 지정하지 않으면 패키지의 Codex 기본 모델을 사용합니다.

## 데이터와 이력

XDG 환경변수가 있으면 해당 절대 경로를 사용하고, 없으면 아래 기본값을 사용합니다.

| 경로 | 내용 |
| --- | --- |
| `~/.local/share/buku-journal/journal/` | 기록과 장소 파일, 독립적인 jj 저장소 |
| `~/.local/share/buku-journal/inbox/` | 아직 처리되지 않았거나 재확인이 필요한 입력 원문 |
| `~/.local/state/buku-journal/jobs/` | 작업 상태와 제한된 실행 진단 |
| `~/.local/state/buku-journal/work/` | Codex 임시 작업 사본 |
| `~/.config/buku-journal/token` | 웹 접속 키 |
| `~/.codex/` 또는 `$CODEX_HOME` | Codex가 관리하는 인증 정보 |

기록은 `YYYY-MM-DD-제목.md`, 장소는 `장소이름.txt`입니다. Markdown 본문은 짧은 문장과 줄바꿈을 유지하고 장소를 `[장소](./장소이름.txt)`로 연결합니다. TXT는 장소 이름, 주소, 지도 URL 순서입니다. 모르는 정보는 빈 줄로 두며 임의로 채우지 않습니다. Frontmatter는 필요하지 않습니다.

최초 실행 전 비어 있는 데이터 저장소로 기존 기록을 가져올 수 있습니다. 원본 디렉터리는 변경하지 않으며 대상에 기록이 있으면 덮어쓰지 않고 거절합니다. 실행 중인 서비스가 있다면 먼저 정지합니다.

```sh
nix run . -- import --from /absolute/path/to/legacy
```

웹에서 자연어로 새 기록을 만들거나 카드의 내용 추가로 기존 기록을 보완할 수 있습니다. 날짜나 대상이 모호하면 추가 질문을 표시합니다. 원문 편집과 jj 변경 이력도 제공합니다. 달력에서 기록이 있는 날짜를 선택하거나 검색어로 피드를 좁힐 수 있습니다.

직접 파일을 저장해도 화면이 주기적으로 다시 읽습니다. 웹 작업이 진행되는 동안 같은 파일을 수정하면 서버는 충돌을 검사하여 오래된 결과가 덮어쓰지 않도록 합니다. 외부 편집은 웹 작업과 구분해 jj 이력에 남깁니다. 여러 에디터가 동시에 같은 파일을 쓰는 것까지 조정하는 공동 편집 프로토콜은 아닙니다.

Codex는 임시 사본에서 작업하고, Go 서버가 실제 변경과 파일 경로를 검사한 후 데이터 저장소에 반영합니다. 작업은 순차 실행됩니다. 실패하거나 취소된 입력은 없어지지 않습니다. 사용자가 직접 `jj`로 관리하는 것도 가능하며 Git 명령은 사용하지 않습니다.

백업할 때는 앱을 잠시 정지하고 데이터 디렉터리 전체를 복사하세요. jj의 `.jj`와 Git backend 메타데이터도 포함해야 이력이 보존됩니다. 작업 복구까지 보존하려면 state 디렉터리도 함께 백업합니다. 인증 정보와 접속 키 백업은 별도로 비공개 보관하세요.

## hooddy 배포

bukupan과 동일하게 systemd **사용자 서비스**와 hooddy의 일반 Caddy 사이트 파일을 사용합니다. hooddy가 이미 설치되어 있고, 사용자 linger가 활성화되어 있어야 합니다. 배포 명령은 앱 설치·갱신, 백엔드 상태 검사, Caddy 전체 설정 검사와 reload를 수행합니다. 배포가 실패하면 이전 서비스·사이트 설정을 복구합니다.

```sh
jj st
nix run .#deploy
```

기본 도메인은 `buku-journal.hooreique.com`, 백엔드는 `127.0.0.1:3002`입니다. 외부 도메인은 해당 hooddy 서버로 연결되어 있어야 합니다. 다른 값을 사용하려면 매번 같은 옵션으로 배포합니다.

```sh
nix run .#deploy -- \
  --domain buku-journal.hooreique.com \
  --port 3002 \
  --journal-dir /absolute/path/to/journal
```

배포가 관리하는 파일:

- `~/.config/systemd/user/buku-journal.service`
- `~/.config/hooddy/sites.d/buku-journal.caddy`
- `~/.local/state/buku-journal/package` — 실행 패키지의 Nix GC root

자신이 생성한 관리 표식이 있는 파일만 교체하며, 기존 일기 파일은 이동·가져오기·삭제하지 않습니다. hooddy의 다른 사이트나 master Caddyfile도 수정하지 않습니다. 서비스는 자동 시작을 활성화합니다. 프록시 설정만 다시 적용하려면 `hooddy restart`를 사용합니다.

```sh
systemctl --user status buku-journal.service
journalctl --user -u buku-journal.service -n 50 --no-pager
systemctl --user restart buku-journal.service
systemctl --user stop buku-journal.service
```

앱 업데이트 후에는 `jj st`와 `nix run .#deploy`를 다시 실행합니다. 데이터와 접속 키는 유지됩니다. 자동 시작까지 중지하려면 `systemctl --user disable --now buku-journal.service`를 사용합니다.

## 개발과 검증

```sh
nix develop
go test ./...
go vet ./...
jj st
nix flake check
nix flake show --all-systems
```

`flake.lock`으로 Nixpkgs를 고정하고 Go 서버, Codex CLI, jj와 작업에 필요한 기본 쉘 도구를 패키징합니다. 웹 파일은 Go 실행 파일에 포함됩니다. 실제 일기와 인증 정보는 Nix 빌드 소스 및 실행 파일에 포함되지 않습니다.

이 저장소는 앱 소스만 관리합니다. `/home/song/buku-journal`의 기존 기록은 legacy 자료로 보존하고, 운영 데이터는 별도의 XDG 데이터 저장소에서 관리합니다.
