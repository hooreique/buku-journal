# 유지보수 및 기여 안내

개발 환경 구성, 운영 설정, 데이터 관리와 배포 절차를 정리합니다. 최초 실행은 [README](README.md)를 참고하세요. 소스와 데이터의 변경 이력은 Git 명령 대신 `jj`로 관리합니다.

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

## 실행 설정

Codex는 서버를 실행하는 사용자의 인증을 사용합니다. 같은 사용자로 `codex login`을 완료하고, 인증 상태는 `codex login status`로 확인하세요. 개별 요청은 사용자 설정과 규칙을 읽지 않는 별도 `codex exec` 작업으로 실행됩니다. 인증 만료나 실행 실패 시 입력은 보존되며 웹에서 다시 시도할 수 있습니다.

브라우저에 Codex 인증 정보가 전달되지 않으며, 기록 API는 로그인한 세션에서만 사용할 수 있습니다. HTTPS 외부 접속은 아래 hooddy 배포 절차를 따릅니다.

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

상대 날짜는 설정한 `timezone`의 최초 제출 시각을 기준으로 해석합니다. 웹의 오늘 표시와 시각 표시는 `Asia/Seoul` 기준입니다.

지원 키는 `journal_dir`, `state_dir`, `listen`, `timezone`, `codex`, `jj`, `model`, `token_file`, `worker_timeout`입니다. 별도 모델을 지정하지 않으면 패키지의 Codex 기본 모델을 사용합니다.

## 데이터와 이력

XDG 환경변수가 있으면 해당 절대 경로를 사용하고, 없으면 아래 기본값을 사용합니다.

| 경로 | 내용 |
| --- | --- |
| `~/.local/share/buku-journal/journal/` | 기록과 장소 파일, 독립적인 jj 저장소 |
| `~/.local/share/buku-journal/inbox/` | 제출 원문과 추가 답변 보관본. 완료 후에도 유지 |
| `~/.local/state/buku-journal/jobs/` | 작업 상태와 제한된 실행 진단 |
| `~/.local/state/buku-journal/work/` | Codex 임시 작업 사본 |
| `~/.config/buku-journal/token` | 웹 접속 키 |
| `~/.codex/` 또는 `$CODEX_HOME` | Codex가 관리하는 인증 정보 |

`inbox/`는 입력 보관용이며 자동 복구 시 읽는 경로는 아닙니다. 작업 상태 복구에는 `jobs/`가 필요합니다. `--journal-dir`을 바꾸면 inbox는 해당 기록 디렉터리의 형제 경로에 생성됩니다.

기록은 `YYYY-MM-DD-제목.md`, 장소는 `장소이름.txt`입니다. Markdown 본문은 짧은 문장과 줄바꿈을 유지하고 장소를 `[장소](./장소이름.txt)`로 연결합니다. TXT는 장소 이름, 주소, 지도 URL 순서입니다. 모르는 정보는 빈 줄로 두며 임의로 채우지 않습니다. Frontmatter는 필요하지 않습니다.

최초 실행 전 비어 있는 데이터 저장소로 기존 기록을 가져올 수 있습니다. 원본 디렉터리는 변경하지 않으며 대상에 기록이 있으면 덮어쓰지 않고 거절합니다. 실행 중인 서비스가 있다면 먼저 정지합니다.

```sh
nix run . -- import --from /absolute/path/to/legacy
```

웹에서 자연어로 새 기록을 만들거나 카드의 내용 추가로 기존 기록을 보완할 수 있습니다. 날짜나 대상이 모호하면 추가 질문을 표시합니다. 원문 편집과 jj 변경 이력도 제공합니다. 달력에서 기록이 있는 날짜를 선택하거나 검색어로 피드를 좁힐 수 있습니다.

직접 파일을 저장해도 화면이 주기적으로 다시 읽습니다. 웹 작업이 진행되는 동안 같은 파일을 수정하면 서버는 충돌을 검사하여 오래된 결과가 덮어쓰지 않도록 합니다. 외부 편집은 웹 작업과 구분해 jj 이력에 남깁니다. 여러 에디터가 동시에 같은 파일을 쓰는 것까지 조정하는 공동 편집 프로토콜은 아닙니다.

Codex는 임시 사본에서 작업하고, Go 서버가 실제 변경과 파일 경로를 검사한 후 데이터 저장소에 반영합니다. 작업은 순차 실행됩니다. 실패하거나 취소된 입력은 없어지지 않습니다. 사용자가 직접 `jj`로 관리하는 것도 가능하며 Git 명령은 사용하지 않습니다.

백업할 때는 앱을 잠시 정지하고 데이터 디렉터리 전체를 복사하세요. jj의 `.jj`와 Git backend 메타데이터도 포함해야 이력이 보존됩니다. 작업 복구까지 보존하려면 state 디렉터리도 함께 백업합니다. 인증 정보와 접속 키 백업은 별도로 비공개 보관하세요.

SSH로 서버 밖에 스냅샷을 보관하려면 [snapshot.sh.template](snapshot.sh.template)을 작업 저장소 밖으로 복사하고, 상단의 SSH 대상과 서비스·데이터 경로를 수정한 뒤 Bash로 실행하세요.

```sh
cp snapshot.sh.template ~/snapshot-buku-journal.sh
# ~/snapshot-buku-journal.sh 상단의 설정을 수정합니다.
bash ~/snapshot-buku-journal.sh
```

서버의 SSH 계정에서 Bash, tar, flock, systemd 사용자 서비스 제어를 사용할 수 있어야 합니다. 실행 중인 서비스를 잠시 멈춰 일기, inbox, state를 하나의 압축 파일로 전송한 뒤 서비스를 다시 시작합니다. 로컬에서 압축 파일을 읽을 수 있는지 확인한 후 기본 경로인 `~/Backups/buku-journal/`에 저장합니다. 압축 파일 내부 경로는 서버의 파일시스템 루트 기준입니다. 웹 접속 키와 Codex 인증 정보는 별도 보관 대상입니다.

## hooddy 0.2.0 배포

서비스를 실행할 사용자의 `~/.config/hooddy/flake.nix`에 앱 입력과 모듈을 추가합니다. 다음은 x86_64 Linux 예시이며 ARM64에서는 `aarch64-linux`를 지정합니다. 기존 설정이 있다면 다른 입력과 모듈은 유지합니다.

```nix
{
  inputs.hooddy.url = "github:hooreique/hooddy";
  inputs.buku-journal.url = "github:hooreique/buku-journal";
  outputs = { hooddy, buku-journal, ... }: {
    hooddyConfigurations.default = hooddy.lib.hooddyConfiguration {
      system = "x86_64-linux";
      modules = [
        buku-journal.hooddyModules.default
        {
          services.buku-journal = {
            enable = true;
            domain = "buku-journal.hooreique.com";
            port = 3002;
            # journalDir = "/absolute/path/to/journal";
          };
        }
      ];
    };
  };
}
```

이전 `nix run .#deploy`로 설치했다면 전환 전에 `systemctl --user cat buku-journal.service`로 경로 설정을 확인하고 `systemctl --user disable --now buku-journal.service`로 기존 서비스를 중지합니다. 같은 사용자로 실행하고 일기, 상태, 토큰 경로를 유지하세요. 사용자 지정 XDG 경로나 `CODEX_HOME`은 새 systemd 사용자 서비스 환경에도 설정해야 합니다. hooddy 0.1의 사이트 백업과 Caddy 전환은 [hooddy 업그레이드 절차](https://github.com/hooreique/hooddy/blob/main/CONTRIBUTING.md#moving-from-01)를 따릅니다.

```sh
cd ~/.config/hooddy
nix flake lock
hooddy switch
systemctl --user status hooddy-buku-journal.service
```

hooddy는 루프백 백엔드의 `/healthz`를 확인하고 Caddy 설정을 적용합니다. 로그는 `journalctl --user -u hooddy-buku-journal.service`에서 확인합니다. 앱 업데이트는 이 디렉터리에서 `nix flake update buku-journal` 후 `hooddy switch`로 적용합니다. 서비스를 내리려면 `services.buku-journal.enable = false;`로 변경하고 다시 전환합니다.
