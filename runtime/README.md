Place external binaries here:

- `ffmpeg` (or `ffmpeg.exe`) — https://ffmpeg.org/download.html (needs libopus, libmp3lame, srt)
- `whisper-server` (or `whisper-server.exe`) — build of whisper.cpp `examples/server`
  (choose the CUDA / Metal / Vulkan / CPU build for your machine)

Subfolders such as `runtime/ffmpeg/bin/ffmpeg` or `runtime/whisper-server/whisper-server` are also searched.
