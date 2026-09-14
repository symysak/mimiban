# Mimiban

Mimiban captures the audio output of radio scanners/receivers through a PC audio input, cuts it into individual calls with a VOX (level-triggered) detector, stores each call as MP3, transcribes it locally with whisper.cpp and lets you search, play back and analyse everything from a browser. Several receivers can run at the same time.

- Everything runs locally. Audio and text never leave your machine (except model downloads and optional Discord notifications).
- Windows 11 / macOS / Linux (including Raspberry Pi 4+). One static Go binary plus a `runtime/` folder.
- Meant for use on your own LAN. There is no authentication.

日本語の詳しい説明は [README.md](README.md) を参照してください。

## Quick start

1. Put the `mimiban` binary in a folder and create `runtime/` next to it.
2. Put **ffmpeg** in `runtime/` (or install it on PATH). It needs `libmp3lame`; `libopus` + `srt` are needed for the remote agent.
3. Optionally put **whisper-server** (whisper.cpp `examples/server`) in `runtime/`. Pick the CUDA / Metal / Vulkan / CPU build for your machine; Mimiban detects GPU use from its log. Without it recording still works, only transcription is disabled.
4. Run:

   ```
   ./mimiban
   ```

   The console prints the UI URL (default `http://0.0.0.0:8000/`) and which binaries were found. Open it from any device on the LAN.

   **Windows firewall**: on the first start Windows Defender Firewall asks whether to allow `mimiban.exe`; tick *Private networks* and click *Allow access*, otherwise phones on the LAN cannot reach the UI (localhost still works). To allow it later, run in an elevated PowerShell (adjust the port if you changed it), and add the receiver's listen port (default 5004, **UDP**) on a PC that receives a remote agent:

   ```powershell
   New-NetFirewallRule -DisplayName "Mimiban UI" -Direction Inbound -Protocol TCP -LocalPort 8000 -Profile Private -Action Allow
   New-NetFirewallRule -DisplayName "Mimiban agent stream" -Direction Inbound -Protocol UDP -LocalPort 5004 -Profile Private -Action Allow
   ```

   If the network is classified as *Public*, inbound connections are blocked by default; switch it to *Private* in Settings → Network & Internet.
5. In **Settings → Models** download a model (for Japanese radio `small (q5_1)` or `kotoba-whisper v2.0`; `Silero VAD` is recommended too), select it and save.
6. In **Record** click **+ Add receiver**, pick the audio device (or a remote agent / a URL such as an RTLSDR-Airband UDP stream), save. Drag the white line on the level meter to set the VOX threshold; it is applied immediately.

Binary lookup order: `runtime/` → explicit path in `config.json` → PATH.

Data layout: `config.json`, `mimiban.sqlite3`, `recordings/<receiver>/<date>/<timestamp>.mp3`, `models/`.

## Remote agent

Run the same binary on a remote PC to stream a receiver to the main instance over SRT/Opus (always-on, no VOX; the main instance does the cutting):

```
mimiban agent --list-devices
mimiban agent --device "USB Audio Device" --to 100.101.102.103:5004
mimiban agent --device "USB Audio Device" --channels 2 --to 100.101.102.103:5004      # stereo L/R
mimiban agent --input-url udp://127.0.0.1:6001 --input-format s16le --input-rate 16000 --to 100.101.102.103:5004
```

There is no authentication; use it inside a VPN such as Tailscale. `--passphrase` enables SRT encryption (set the same value on the receiver).

## Build

Go 1.22+, no cgo.

```
make build   # this machine
make test
make cross   # windows/amd64 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 → dist/
```

## Notes

- Do not expose the UI to the Internet; it has no authentication.
- Check the laws that apply to receiving and recording radio traffic where you live (in Japan: Radio Act Article 59 forbids disclosing or exploiting the content of communications addressed to others).
- Transcripts are machine generated and contain errors.
