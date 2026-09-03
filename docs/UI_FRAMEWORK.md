# 웹 화면 구조

기존 화면은 Alpine.js와 일반 JavaScript 모듈로 동작했다. 현재 사용자가 여는 작업 화면은 Vue 3으로 통합되어 있으며, 기존 모듈은 기능을 옮기는 동안 유지한다.

## Vue 모니터

Vue 3/Vite로 옮긴 작업 관제는 `/ui/monitor`에서 확인한다. `/ui/monitor-vue`와 `/ui/monitor-alpine`은 기존 주소를 위한 호환 경로이며 같은 Vue 화면을 연다.

```bash
cd webui-vue
npm ci
npm run build
```

빌드 결과는 `internal/web/ui/vue-monitor.js`에 임베드된다. Go 서버를 실행한 뒤 다음 주소를 연다.

```text
http://127.0.0.1:8080/ui/monitor
```

프로젝트·검색·상태·에이전트 필터와 선택 작업 상세는 URL 쿼리와 함께 유지된다. 테마는 `stash.theme` 설정과 시스템 색상 설정을 따른다. 원격 MCP는 OAuth 인증 코드로 발급한 리소스 전용 접근 토큰이나 Stash API 토큰을 사용하고, 화면은 브라우저 세션을 사용한다.

## 옮기는 순서

1. 모니터에서 데이터 조회·필터·URL 상태를 검증한다.
2. 목표 지도와 작업 계획을 같은 방식으로 Vue 화면으로 옮긴다.
3. 공통 상태를 Pinia 또는 작은 MVVM 저장소로 분리한다.
4. 기존 Alpine 경로를 제거하기 전에 화면별 사용 확인과 Go 임베드 검사를 통과시킨다.

Vite는 배포 전에 정적 번들을 만들고, Go 서버는 그 결과를 계속 임베드한다. 따라서 운영 서버에 Node를 설치할 필요는 없다.
