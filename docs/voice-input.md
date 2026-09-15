# Chat 语音输入与图片

## 图片

在 Chat 输入框粘贴、拖入或选择图片后，先显示缩略图并上传到当前远程主机暂存。暂存不会向终端输入路径或回车；点击发送后，图片引用与文字合并提交一次，支持只发图片。可以移除未发送的附件。移除缩略图不等于删除远程暂存文件。

仍在上传时不能发送。上传失败的附件显示原因，不会被提交；可以重试或移除，也可继续发送文字及已就绪图片，失败占位会保留。输入提交失败时保留草稿和附件；结果不确定时先到终端确认，不自动重放。附件按 Web 登录、主机与 pane 隔离。Agent 能否理解图片由其自身能力决定。

## 配置语音网关

默认关闭。支持 **OpenAI Realtime 兼容的独立 WebSocket 会话协议**，不是任意语音服务或 Gemini Live 的通用适配器。上游需支持输入音频转写；独立的 `/audio/transcriptions` 接口不是本实现的必需条件。

在部署目录建立权限为 `0600` 的 `voice.env`，通过 `docker run --env-file ./voice.env` 注入，或在自己的 Compose 覆盖文件使用 `env_file`。密钥只放服务器配置，不能放前端构建变量、公开仓库或浏览器代码。

```dotenv
HERDRX_VOICE_ENABLED=true
HERDRX_VOICE_BASE_URL=https://gateway.example.com
HERDRX_VOICE_API_KEY=replace-with-your-server-side-key
HERDRX_VOICE_MODEL=gpt-realtime
HERDRX_VOICE_REALTIME_PATH=/v1/realtime
HERDRX_VOICE_AUDIO_REPLY=off
```

`BASE_URL` 必须是完整 HTTP(S) origin，不含路径、查询串或凭据。启用但缺少网关地址或密钥时，网站启动会报错，而不是悄悄关闭。修改后重建网站容器，保留原数据 bind mount 和其他配置；无需重启远程 Herdr。

`HERDRX_VOICE_AUDIO_REPLY=allowed` 允许用户在界面主动开启语音回复，默认仍是文字模式。回复来自独立语音助手，不是 Claude/Codex 终端 Agent；语音助手看不到或控制不了终端。识别到的用户语音只追加到草稿，必须手动发送。

## 使用与隐私

- 浏览器需 HTTPS 或 localhost，首次点击麦克风时授权；页面加载不会自动录音。
- 停止、切回 Terminal、离开页面或退出登录会关闭麦克风、音频上下文和语音连接。
- 录音会发送给管理员配置的上游服务；上游的存储与隐私政策由该服务决定。
- 浏览器只连接本站，不能指定任意网关 URL、模型或密钥；请求经过登录、Origin、CSRF 和一次性票据校验。
- 不记录音频和转写正文到应用审计；审计只记录语音会话操作。

## 资源上限

| 变量 | 默认值 | 含义 |
|---|---:|---|
| `HERDRX_VOICE_MAX_SESSIONS_PER_USER` | 1 | 每位用户同时语音会话数 |
| `HERDRX_VOICE_MAX_SESSIONS` | 4 | 全实例同时语音会话数 |
| `HERDRX_VOICE_MAX_SESSION_SECONDS` | 600 | 单次连接最长秒数 |
| `HERDRX_VOICE_IDLE_SECONDS` | 30 | 无活动超时秒数 |
| `HERDRX_VOICE_MAX_UPLINK_AUDIO_SECONDS` | 120 | 单次上行音频秒数 |
| `HERDRX_VOICE_MAX_UPLINK_BYTES` | 8388608 | 单次上行音频字节数 |
| `HERDRX_VOICE_CREATE_PER_MINUTE` | 10 | 每用户每分钟创建频率 |
| `HERDRX_VOICE_INPUT_SAMPLE_RATE` | 24000 | 上行 PCM16 采样率 |
| `HERDRX_VOICE_OUTPUT_SAMPLE_RATE` | 24000 | 下行 PCM16 采样率 |

上游模型可能要求固定采样率；不要仅因服务端接受配置值就认为上游也支持。没有麦克风按钮时先检查是否启用语音；授权失败检查安全上下文、站点权限及反向代理是否覆盖 `Permissions-Policy`。连接失败、配额不足或转写失败时页面会显示可执行的错误提示，不自动重发音频。
