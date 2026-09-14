# Mimiban

無線受信機の音声を PC の音声入力から取り込み、VOX（音声レベル連動）で交信ごとに切り出して MP3 保存し、ローカル AI（whisper.cpp）で文字起こしして、ブラウザから検索・再生・分析する Web アプリです。複数の受信機を同時に扱えます。

- 全処理はローカル完結。音声・テキストを外部サーバーへ送信しません（モデルのダウンロードと Discord 通知を除く）。
- 対応 OS: Windows 11 / macOS / Linux（Raspberry Pi 4 以上含む）。単一バイナリ + `runtime/` フォルダで動きます。
- 個人の LAN 内利用を前提とし、認証はありません。

English: [README.en.md](README.en.md)

## 目次

1. [セットアップ](#セットアップ)
2. [使い方](#使い方)
3. [遠隔エージェント](#遠隔エージェントmimiban-agent)
4. [RTLSDR-Airband 連携例](#rtlsdr-airband-連携例)
5. [Discord 通知](#discord-通知)
6. [ファイル構成](#ファイル構成)
7. [ビルド](#ビルド)
8. [注意事項](#注意事項)

## セットアップ

### 1. バイナリを置く

配布物（またはビルド結果）の `mimiban`（Windows は `mimiban.exe`）を任意のフォルダに置きます。同じフォルダに `runtime/` を作り、外部バイナリを入れます。

```
mimiban/
├── mimiban            # 本体
├── runtime/
│   ├── ffmpeg         # 必須
│   └── whisper-server # 文字起こしに使う（無くても録音はできる）
├── models/            # ggml モデル（UI からダウンロード）
├── config.json        # 初回起動時に自動生成
├── mimiban.sqlite3    # 交信ログ DB
└── recordings/        # 録音 MP3
```

外部バイナリの探索順は **`runtime/` → `config.json` の明示パス → PATH** です。`runtime/ffmpeg/bin/ffmpeg` のようなサブフォルダも探します。

### 2. ffmpeg を入手する

- Windows: https://www.gyan.dev/ffmpeg/builds/ の「release essentials」を展開し、`bin/ffmpeg.exe` を `runtime/ffmpeg.exe` に置く
- macOS: `brew install ffmpeg`（PATH に入るので `runtime/` は不要）
- Linux / Raspberry Pi: `sudo apt install ffmpeg`

必要なもの: `libmp3lame`（MP3 化）、`libopus` と `srt` プロトコル（遠隔エージェントを使う場合）。公式ビルド・各ディストリの ffmpeg には通常含まれています。`ffmpeg -protocols | grep srt` で確認できます。SRT が無い ffmpeg では自動的に UDP にフォールバックします。

### 3. whisper-server を入手する

whisper.cpp の `examples/server`（バイナリ名 `whisper-server`）を使います。

- macOS: `brew install whisper-cpp`（Metal GPU 対応、PATH に入る）
- Windows: https://github.com/ggml-org/whisper.cpp/releases から `whisper-bin-x64.zip`（CPU）または `whisper-cublas-*.zip`（NVIDIA GPU）を展開し、中の `whisper-server.exe` と同梱 DLL をまとめて `runtime/whisper-server/` に置く
- Linux: ソースからビルド（`cmake -B build -DGGML_CUDA=ON` など）。`build/bin/whisper-server` を `runtime/` に置く

**GPU 版の選び方**: Go 本体は GPU を関知しません。whisper-server バイナリを CUDA / Metal / Vulkan / CPU 版に差し替えるだけで切り替わります。起動ログに `ggml_cuda_init` / `ggml_metal_init` / `ggml_vulkan` が出れば GPU 使用中と判定し、設定タブに「GPU: 使用中 / CPU のみ」を表示します。

### 4. 起動

```
./mimiban
```

コンソールに URL（既定 `http://0.0.0.0:8000/`）と ffmpeg / whisper-server の検出結果が出ます。同じ LAN のスマホからは `http://<PCのIP>:8000/` で開けます。

オプション: `mimiban serve --dir <データフォルダ> --port 8000 --host 0.0.0.0`

### 5. モデルをダウンロードする

設定タブ →「モデル」で、使うモデルの「ダウンロード」を押します（Hugging Face から取得、SHA-256 で検証）。日本語無線なら `small (q5_1)` または `kotoba-whisper v2.0 日本語特化 (q5_0)` がおすすめです。`Silero VAD` も入れておくと無音区間を飛ばして幻覚が減ります。取得後、「モデル」の選択肢で選んで「設定を保存」。

## 使い方

### 録音タブ

1. 「＋ レシーバーを追加」を押し、名前・入力元・デバイスを選んで保存。保存すると自動で受信を開始します。
2. レベルメーター上の白い縦線が VOX 閾値です。**ドラッグで即反映**されます。無線の無音時（スケルチ閉）のレベルより 5〜10 dB 上に置くのが目安です。
3. 音声が閾値を超えると録音開始、`silence_sec`（既定 1.5 秒）無音で終了。直前 `prebuffer_sec` 分を先頭に含めます。
4. 保存された交信は「最近の受信」に出ます。文字起こしは順番に処理されます。

**ステレオの L/R 分離**: 受信機 2 台をステレオプラグの左右に接続している場合、同じデバイスで「左」「右」のレシーバーを 2 つ登録できます。ffmpeg プロセスは 1 本で、ブロックごとに左右へ配ります。「モノ」と「左/右」の併用や「左」の二重登録は拒否されます。

**状態ドット**: 停止 = 灰 / 待機（未接続）= 灰点線 / 待受 = 緑 / 録音中 = 赤点滅 / エラー = 橙。USB を抜くとエラーになり、5 秒間隔で再接続を試みます。

**同報系（防災行政無線など）の VOX 設定**: 文と文の間に 1〜2 秒の間があるため、既定の無音 1.5 秒では文ごとに細切れになります。`silence_sec` を **2.5〜3 秒**、閾値を放送の背景ノイズより少し上（例 -45 dB）にすると 1 放送 = 1 交信になり、文字起こしの精度も上がります。交互通話（消防・鉄道など）は既定値のままで構いません。

### ログタブ

日付・レシーバー（複数選択）・キーワード（文字起こしの部分一致）で絞り込み。レシーバー横断で検索できます。再生・ダウンロード・修正・再認識・削除ができます。

### 分析タブ

期間（7/30/90/365 日）とレシーバーを選び、交信数・受信時間・平均長と、時間帯別 / 曜日別 / 日別 / レシーバー別のグラフを表示します。

### 設定タブ

- 全般: 待受ホスト・ポート、録音保存先、MP3 ビットレート、保持日数（0 = 無制限。超えたものは 1 時間ごとに DB・ファイルとも削除）
- 文字起こし: モデル、言語、スレッド数、アイドル終了（分。whisper-server は最初の交信で起動し、アイドルが続くと終了して RAM を返します）、ヒント文、定番幻覚句
- 用語辞書: 誤変換 → 正しい表記。「保存して既存の結果に適用」で再認識なしに一括置換
- Discord Webhook と通知ログ
- UI 言語: 自動 / 日本語 / English

### モデルの選び方（実測）

Apple M3 / Metal で 82 秒の防災行政無線を文字起こしした結果です。処理時間は whisper-server の起動込み。

| モデル | サイズ | 時間 | 所感 |
|---|---|---|---|
| tiny (q5_1) | 32 MB | 数秒 | 日本語は実用外。動作確認のみ |
| small (q5_1) | 190 MB | 数秒 | 文意は取れるが「先着順→選着順」「抽選券→中線県」など誤変換が多い |
| medium (q5_0) | 539 MB | 8 秒 | 一般語はほぼ正確。地名はまだ揺れる |
| large-v3-turbo (q5_0) | 574 MB | 8 秒 | medium と同等以上で速い。**まず試すならこれ** |
| large-v3 (q5_0) | 1.1 GB | 13 秒 | 最も正確。ただし大きなモデルほど末尾に「ご視聴ありがとうございました」を付けやすい（文単位で自動除去） |

GPU が無い PC（Raspberry Pi など）では small か base を選び、遅延を許容するなら medium までが現実的です。固有名詞（地名・部署名）はどのモデルでも揺れるので用語辞書で吸収してください。

### 幻覚対策

whisper はスケルチノイズだけの短い音声に「ご視聴ありがとうございました」等の定番句を出すことがあります。Go 側で以下を必須で適用します。

1. 同じ文・語句が 3 回以上連続 → 空文字にして `repetition`
2. セグメントの `no_speech_prob` が閾値（既定 0.7）超え → そのセグメントを捨てる
3. 結果が定番幻覚句だけ → 空文字（句リストは言語別・編集可）。ヒント文をそのまま返した場合も同様
4. 「(音楽)」「(拍手)」のような括弧だけの結果 → 空文字
5. 本文の後ろに付いた「ご視聴ありがとうございました。」のような文は、その文だけ削除して残りを保存

## 遠隔エージェント（`mimiban agent`）

受信機が別の場所にある場合、その PC で同じバイナリを送信専用モードで動かし、音声を本体へストリームします。エージェントは DB・UI・ASR・設定ファイルを持ちません。

```
# デバイス一覧
mimiban agent --list-devices

# 1ch で送る
mimiban agent --device "USB Audio Device" --to 100.101.102.103:5004

# ステレオ L/R をそのまま 2ch で送る（本体側で左・右レシーバーを 2 つ作る）
mimiban agent --device "USB Audio Device" --channels 2 --to 100.101.102.103:5004

# SDR ソフトの UDP 出力を中継
mimiban agent --input-url "udp://127.0.0.1:6001" --input-format s16le --input-rate 16000 --to 100.101.102.103:5004

# SRT パスフレーズ（本体側レシーバーにも同じ値を設定）
mimiban agent --device "USB Audio Device" --to 100.101.102.103:5004 --passphrase "my-secret-phrase"
```

- 送出は常時（VOX なし）。無音区間も流し、切り出しは本体側で行います。Opus 32 kbps（1ch）/ 48 kbps（2ch）、`--bitrate` で変更可。
- ffmpeg が終了したら 5 秒後に再起動。本体が落ちていても再接続し続けます。
- **認証はありません。Tailscale 等の VPN 内で使う前提です。** SRT パスフレーズは暗号化のみで、認証の代わりにはなりません。

本体側: レシーバー追加で入力元「遠隔エージェント」を選ぶと待受ポートとパスフレーズの欄が出て、遠隔 PC で打つコマンドが表示されます（本体の Tailscale IP は `tailscale ip -4` が使えれば自動で入ります）。

### サービス化

**Linux (systemd)** — `/etc/systemd/system/mimiban-agent.service`:

```ini
[Unit]
Description=Mimiban agent
After=network-online.target sound.target

[Service]
ExecStart=/opt/mimiban/mimiban agent --device "plughw:1,0" --to 100.101.102.103:5004
Restart=always
RestartSec=5
User=pi

[Install]
WantedBy=multi-user.target
```

```
sudo systemctl daemon-reload && sudo systemctl enable --now mimiban-agent
```

**macOS (launchd)** — `~/Library/LaunchAgents/jp.mimiban.agent.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>jp.mimiban.agent</string>
  <key>ProgramArguments</key><array>
    <string>/Users/you/mimiban/mimiban</string><string>agent</string>
    <string>--device</string><string>USB Audio Device</string>
    <string>--to</string><string>100.101.102.103:5004</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/mimiban-agent.log</string>
</dict></plist>
```

```
launchctl load ~/Library/LaunchAgents/jp.mimiban.agent.plist
```

**Windows (タスクスケジューラ)**: 「タスクの作成」→ トリガー「ログオン時」（または「スタートアップ時」）→ 操作「プログラムの開始」で `C:\mimiban\mimiban.exe`、引数に `agent --device "マイク (USB Audio Device)" --to 100.101.102.103:5004`。「タスクが失敗した場合の再起動の間隔」を 1 分に設定。

本体（`mimiban serve`）も同じ方法でサービス化できます。

## RTLSDR-Airband 連携例

1 本の RTL-SDR ドングルで複数周波数を同時復調し、チャンネルごとに UDP へ出して Mimiban の url レシーバーで受けます。Mimiban は SDR を直接制御しません。

例: 60 MHz 帯の市町村防災行政無線（同報系）4 チャンネル。

`rtl_airband.conf`:

```
devices: ({
  type = "rtlsdr";
  index = 0;
  gain = 28;
  centerfreq = 60.500;
  correction = 0;
  channels: (
    { freq = 60.275; modulation = "nfm";
      outputs: ( { type = "udp_stream"; dest_address = "127.0.0.1"; dest_port = 6001; continuous = true; } ); },
    { freq = 60.425; modulation = "nfm";
      outputs: ( { type = "udp_stream"; dest_address = "127.0.0.1"; dest_port = 6002; continuous = true; } ); },
    { freq = 60.575; modulation = "nfm";
      outputs: ( { type = "udp_stream"; dest_address = "127.0.0.1"; dest_port = 6003; continuous = true; } ); },
    { freq = 60.725; modulation = "nfm";
      outputs: ( { type = "udp_stream"; dest_address = "127.0.0.1"; dest_port = 6004; continuous = true; } ); }
  );
});
```

`udp_stream` は **32 bit float、モノラル、8000 Hz** の生 PCM を送ります（RTLSDR-Airband の `WAVE_RATE`）。`continuous = true` にして無音も流し、切り出しは Mimiban の VOX に任せます。

Mimiban 側のレシーバー設定（4 つ作る）:

| RTLSDR-Airband | Mimiban レシーバー |
|---|---|
| `dest_port = 6001` (60.275 MHz) | 名前 `防災 A`、入力元 **URL**、入力 URL `udp://127.0.0.1:6001`、入力形式 `f32le`、レート `8000`、ch `1` |
| `dest_port = 6002` (60.425 MHz) | 名前 `防災 B`、入力 URL `udp://127.0.0.1:6002`、他は同じ |
| `dest_port = 6003` (60.575 MHz) | 名前 `防災 C`、入力 URL `udp://127.0.0.1:6003` |
| `dest_port = 6004` (60.725 MHz) | 名前 `防災 D`、入力 URL `udp://127.0.0.1:6004` |

Mimiban が ffmpeg で 16 kHz s16le へ変換し、以降はデバイス入力と同じ VOX / 録音 / 文字起こしを行います。同じ URL を複数レシーバーに割り当てることはできません。

Icecast 出力（`type = "icecast"`）を使う場合は入力 URL に `http://host:8000/mount.mp3` を指定し、入力形式は空（自動判定）にします。RTLSDR-Airband を別の PC で動かしている場合は、その PC で `mimiban agent --input-url udp://127.0.0.1:6001 --input-format f32le --input-rate 8000 --input-channels 1 --to <本体>:5004` を回して本体の stream レシーバーで受ける方法もあります。

## Discord 通知

設定タブ → 通知で「通知を有効にする」を入れ、Webhook を追加します（Discord のチャンネル設定 → 連携サービス → ウェブフック → URL をコピー）。

- 送信タイミング: 保存直後 / 文字起こし完了後（既定）/ 両方（保存直後に送って完了後に同じメッセージを編集）
- 対象レシーバー・キーワード（含む / 含まない）・最短長・毎分上限（超えた分は次の通知に「他に N 件」としてまとめる）
- 音声添付（8 MB 以下）
- 通知本文のリンク `http://<本体>/#/calls/<id>` を開くとログタブでその交信が選択されます。リンク先は `notify.base_url` で指定、空なら Tailscale IP から自動生成
- `generic_json` タイプは `{event, call, receiver, audio_url, url}` を POST するだけ。n8n / Home Assistant 等との連携用
- 結果は通知ログ（直近 50 件）に残ります。失敗しても録音・文字起こしには影響しません

## ファイル構成

```
main.go                  CLI（serve / agent）
internal/config          config.json の読み書き
internal/capture         ffmpeg 子プロセス、OS 別デバイス列挙、入力引数生成
internal/vox             VOX 状態機械
internal/engine          レシーバー管理、共有キャプチャ、MP3 化、ASR ワーカー、保持期間
internal/asr             whisper-server 管理・クライアント、幻覚フィルタ、辞書、モデル DL
internal/store           SQLite（calls / notify_log / 統計）
internal/notify          Discord / generic_json 通知キュー
internal/api             JSON API
internal/agent           mimiban agent
internal/i18n            通知テンプレート（ja / en）
web/index.html           UI（依存なし・埋め込み）
web/locales/{ja,en}.json UI 翻訳辞書
```

録音: `recordings/<receiver_id>/<YYYY-MM-DD>/<YYYYMMDD_HHMMSS_mmm>.mp3`（モノラル 16 kHz、既定 48 kbps）

## ビルド

Go 1.22 以上。cgo 不要。

```
make build          # このマシン向け
make test           # 単体テスト
make cross          # windows/amd64, darwin/arm64, darwin/amd64, linux/amd64, linux/arm64 を dist/ へ
```

## 注意事項

- **LAN 内利用専用です。** 認証が無いため、インターネットに直接公開しないでください。外出先からは Tailscale 等の VPN 経由で。
- **電波法第 59 条**: 特定の相手方に対して行われる無線通信を傍受してその存在若しくは内容を漏らし、又はこれを窃用してはなりません。録音・文字起こしの内容の取り扱いには十分注意してください。
- 文字起こしは AI による自動生成で、誤りを含みます。
- 音声ファイルは削除操作・保持期間以外では消えません。ディスク容量は設定タブで確認できます。
