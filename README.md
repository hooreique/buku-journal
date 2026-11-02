# 부끄일기 · Buku Journal

두 사람의 기억을 짧은 문장으로 남기는 비공개 일기입니다. Codex가 자연어 입력을 정리하고, 웹에서 기록과 달력을 확인할 수 있습니다.

## Getting started

Nix를 사용할 수 있는 Linux(`aarch64-linux`, `x86_64-linux`)에서 실행합니다.

```sh
nix develop
codex login                         # 서버를 실행할 사용자의 Codex 인증
jj st                              # 새 파일을 Nix 소스에 포함
nix run . -- serve                 # 기본 127.0.0.1:3002
```

브라우저에서 `http://127.0.0.1:3002`에 접속하고, 처음 실행할 때 생성되는 접속 키로 로그인합니다. 접속 키는 비밀번호처럼 보관하세요.

```sh
cat "${XDG_CONFIG_HOME:-$HOME/.config}/buku-journal/token"
```

개발·검증, 실행 설정, 데이터 관리와 배포는 [CONTRIBUTING.md](CONTRIBUTING.md)를 참고하세요.

Android Chrome에서는 HTTPS 주소로 접속한 뒤 메뉴의 앱 설치를 사용합니다. 앱 이름은 `부끄저널`, 짧은 이름은 `bukuj`입니다. 이전에 만든 홈 화면 바로가기는 삭제하고 다시 설치하세요. 설치용 manifest와 아이콘은 로그인 없이 제공됩니다.
