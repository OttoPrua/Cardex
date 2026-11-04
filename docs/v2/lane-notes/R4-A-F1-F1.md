# R4-A-F1-F1

## 做了什么

已存设计节点即使父关系成环、自指或指向环上节点，当前工作的 HTML 和 JSON 也各显示一次。parent 保持库里的值，展示子列表只断开一条回边，无环的链仍嵌在父节点下。卡详情在单条 attempt 事件的 data 不是合法 JSON 时仍返回 200：合法 JSON（对象、数组、数字、null、字符串）原样保留，其他非空文本编成 JSON 字符串，空 data 仍是 {}。outcome、退出码和起止提交照常返回。

## 没做什么，以及原因

未改 store，未拒绝成环父节点或非 JSON 事件，也未改写已存 parent_id 或事件行。未加写路由、登录或前端库。未处理 N7、N8 以外的问题。未改 cmd、其他包，以及其他 lane note。

## 接口请求

无。

## 已知问题

历史仍先读全表再分页。日志仍只返回末尾 32KiB。

## 修复记录

已存设计节点父关系成环、自指或指向环上节点时仍各出现一次，parent 保持原值，展示只断开回边 → internal/board/data.go:210, internal/board/data.go:216, internal/board/data.go:514, internal/board/data.go:539 → TestCyclicDesignParentsStayOnBoard
单条 attempt 事件 data 不合法时卡详情仍成功，合法 JSON 原样保留，其他非空文本编成 JSON 字符串，空 data 仍是 {} → internal/board/data.go:366, internal/board/data.go:568 → TestMalformedAttemptEventKeepsCardDetail
