# 웹 화면 구조

`internal/web/embed.go`가 `/`와 `/ui/*` 작업 화면을 `vue-console.html`로 연결한다. 실제 화면은 Vue 3 통합 콘솔이며, 이전 Alpine 화면과 개별 모니터 소스가 남아 있어도 현재 주소에서 그 화면을 사용하는 것은 아니다.

| 역할 | 파일 | 담당 |
| --- | --- | --- |
| View | `internal/web/ui/vue-console.js` | 배치, 입력과 클릭 연결 |
| 번역 | `internal/web/ui/console-i18n.js` | 한국어·영어 문구, 수량 표현, 언어 선택 |
| ViewModel | `internal/web/ui/vue-console-view-model.js` | 로그인 확인, 화면 상태, 목록 조회, 목표·작업·기억 선택 |
| 데이터 접근 | `internal/web/ui/api-client.js` | MCP 호출, 오류 처리, 목록 페이지 처리 |
| 주소 상태 | `internal/web/ui/route-state.js` | 작업 공간, 검색 조건, 선택 항목, 상세 화면 주소 |
| 지도 배치 | `goal-map-layout.js`, `work-graph-layout.js` | 기존 배치·필터 함수 |
| 스타일 | `internal/web/ui/vue-console.css` | 데스크톱과 모바일 배치 |

ViewModel은 기존 API와 배치 함수를 주입받는다. 별도 상태 관리 라이브러리는 사용하지 않는다. 서버 응답이 돌아왔을 때 현재 요청인지 확인해, 이전 작업 공간의 응답이 새 화면을 덮어쓰지 않도록 한다.

로그인이 필요한 서버에서는 `/auth/status` 확인 후 데이터를 조회한다. 로그인하지 않았거나 세션이 만료되면 로그인 화면을 표시한다. 화면은 브라우저 세션으로 요청하며 API 토큰을 브라우저 저장소에 보관하지 않는다.

화면 모드와 언어는 사이드바에서 선택한다. 언어는 브라우저 저장소의 `stash.locale`, 지원하는 브라우저 언어, 한국어 순서로 정한다. 로그인 화면에서도 언어를 바꿀 수 있다. 번역 모듈은 초기 화면 설정보다 먼저 불러오며, 선택 언어를 문서의 `lang`과 제목에 반영한다. 언어 변경은 표시만 갱신하므로 현재 주소, 검색 조건과 선택한 항목을 유지하고 데이터를 다시 요청하지 않는다. 저장된 작업·기억 내용은 번역하지 않는다.

문구는 `t('키', { count, ... })`로 읽는다. 오류와 처리 결과도 문구 대신 키와 값을 보관해 언어 변경을 반영한다. 숫자와 영어 단·복수는 `Intl`로 처리한다. 새 문구는 두 언어에 같은 키와 변수로 추가한다. 에이전트 규칙도 같은 번역 파일에서 관리하며 코드 식별자는 유지한다. 별도 번역 라이브러리는 사용하지 않는다.

검사 명령:

```bash
node --test internal/web/*.test.cjs
npm run build --prefix webui-vue
go test ./internal/web ./internal/models
```

Vite 빌드 결과인 `internal/web/ui/vue-monitor.js`가 Vue 실행 코드를 제공한다. 통합 콘솔 파일과 함께 Go 서버에 포함되므로, 화면 파일 변경도 운영 반영하려면 Go 서버를 다시 빌드하고 배포해야 한다. 운영 서버에 Node를 설치할 필요는 없다.

현재 기능 차이와 후속 수정 방향은 [UI/UX 검토 보고서](UI_UX_REVIEW.md)를 참고한다.
