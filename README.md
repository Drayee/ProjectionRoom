# ProjectionRoom

鍍?B 绔欐斁鏄犲涓€鏍风殑銆屼竴璧风湅銆嶆埧闂达細**涓绘挱鎾斁鏈湴瑙嗛锛岃浼楀悓姝ヨ鐪嬪苟鑱婂ぉ**銆?
鏈嶅姟鍣ㄥ彧鍋?*淇′护浜ゆ崲銆佹埧闂寸姸鎬佷笌鎷撴墤绠＄悊**鈥斺€斿畠涓嶄紶杈撲换浣曡棰戝瓧鑺傘€傝棰戝垎鐗囩敱涓绘挱閫氳繃
WebRTC DataChannel 鐩存帴鍒嗗彂缁欏叾浠栬妭鐐癸紙P2P 鏍戠姸鍒嗗彂 + 澶氱埗鏉″甫鍖栵級銆?
璁捐涓庡彇鑸嶇殑瀹屾暣渚濇嵁瑙?[`docs/SPEC.md`](docs/SPEC.md)锛涘疄鐜颁腑浠讳綍鍋忕 SPEC 鐨勫湴鏂归兘搴斿厛鏀归偅閲屻€?
**绠楁硶璇存槑**锛堝悓姝ョ畻娉?+ 鏁版嵁閾捐矾锛屽惈鏃跺簭鍥句笌宸茬煡鍋忓樊锛夎 [`docs/ALGORITHM.md`](docs/ALGORITHM.md)銆?
---

## 褰撳墠杩涘害

| 閲岀▼纰?| 鍐呭 | 鐘舵€?|
| :--- | :--- | :--- |
| M1 | 鎴块棿 / 淇′护 / 鑱婂ぉ / 鎴愬憳鍒楄〃 / 鎴夸富鎺у埗 | 鉁?宸查獙璇?|
| **M2** | **鍒嗙墖宸ュ叿 + WebRTC 鍒嗗彂 + MediaSource 鎾斁 + 鎾斁鍚屾** | 鉁?**宸查獙璇侊紙鐪熷疄娴忚鍣ㄥ疄娴嬶級** |
| **M3** | **澶氬眰鏍?+ 鍗曢摼鍒嗗彂妯″紡 + 澶氱埗鏉″甫鍖?+ 鎶楁參鑺傜偣** | **瀹炵幇涓?*锛圓/B 宸茬湡鏈洪獙璇侊紱C/D 寰呴獙璇侊級 |
| M4 | 绋冲仴鎬с€佺洃鎺ч潰鏉裤€丼TUN/TURN 鎺ュ叆 | 寰呭紑濮?|

### 鍚姩闂ㄦ帶锛堝姞杞戒笉璁捐秴鏃讹級

鏂拌妭鐐规帴鍏ユ垨璺宠浆鍚?*涓嶇珛鍗虫挱鏀?*锛岃€屾槸杩涘叆"鍔犺浇涓?锛氱紦鍐茶揪鍒?4 鐗?/ 8 绉掋€?涓旀椂閽熷亸绉讳及璁″凡鏀舵暃锛堟牱鏈?鈮?6 涓旀渶灏忓€?0.8s 鍐呬笉鍐嶄笅闄嶏級鎵嶅紑闂搞€?**闂ㄦ帶涓嶈瓒呮椂涓婇檺**锛氫笂娓告病鏁版嵁灏变竴鐩寸瓑锛岀瓑寰呰秴杩?20s 鏃舵彁绀?涓婃父甯﹀鍙兘涓嶈冻"銆?杩欎慨鎺変簡涓婁竴杞?璧锋挱鍚?0.2鈥?.4s 鍋忓樊灏栧嘲"鐨勬牴鍥狅紙钖勭紦鍐茬‖鎾?鈫?绔嬪埢瑙﹀彂澶у箙鐭锛夈€?
### M3 宸茶惤鍦帮紙宸查獙璇侊級

- **`internal/usecase`**锛氭嫇鎵戝垎閰嶅紩鎿庛€傚厛鎸夋繁搴︽嫨鐖讹紙鏍戣秺娴呰秺濂斤級銆佸悓娣卞害鍐嶆瘮浣欓噺/RTT/绋冲畾鎬э紱
  鍗曢摼妯″紡鎸夊疄娴嬩笂琛岄€変妇鍒嗗彂鑺傜偣锛屽苟甯?1.5 鍊嶆崲闃叉粸鍥烇紙閬垮厤涓や釜宸笉澶氱殑鑺傜偣鏉ュ洖鎶綅锛夛紱
  鎺掑竷瀹归噺涓?*鍑嗗叆瀹归噺**鍒嗗紑绠?鈥斺€?鏈疄娴嬬殑鑺傜偣鍙兘鍙備笌鎺掑竷锛屼笉鑳藉嚟鐚滄祴寮€闂搞€?- **閫愯烦鏃堕挓涓户**锛氫腑缁ц浆鍙戣繘搴︽椂鏀瑰啓 `parentClockMs`锛堣嚜宸辩殑鏃堕挓锛変笌 `parentOffsetMs`锛堣嚜宸卞埌涓绘挱鐨勫亸绉伙級锛?  瀛愯妭鐐规妸鍋忕Щ鍋氭垚"鏈烦 + 鐖惰妭鐐?**涓ょ骇鐩稿姞**銆備慨澶嶄簡"澶氳烦鍙跺瓙鐨勫亸宸?= 鏁存潯璺緞鏈€灏忓崟鍚戝欢杩?
  杩欎釜缁撴瀯鎬у亸宸細瀹炴祴娣卞害 1/2/3 鐨勫亸宸?max 鍒嗗埆涓?**78 / 58 / 74 ms**锛?  鑰屼慨澶嶅墠娣卞害 2 绋冲畾婊炲悗 ~350ms銆?  涓ゆ潯閰嶅瑙勫垯鏄繀闇€鐨勶細**鍙湁涓荤埗鐨勮繘搴︾畻鏉冨▉**锛堝鐢ㄧ埗涓庢崲闃插墠閬楃暀鐨勭洿杩炰細閫佹潵鍙︿竴濂楀彛寰勭殑鏍锋湰锛夛紝
  **鎹㈢埗鍗充綔搴熸湰璺虫牱鏈?*锛堝畠浠槸鐩稿鏃х埗鏃堕挓娴嬬殑锛岀暀鐫€浼氭瘡灞傛亽瀹氬亸鎺変竴涓椂閽熷師鐐瑰樊锛夈€?- **鑷€傚簲鏉″甫鍖?*锛氬彧鍚?鏄庣‘澹版槑鎷ユ湁璇ョ墖"鐨勭埗鑺傜偣鍙栨暟锛涗富鐖剁Н鍘嬫椂鎵嶅垎娴佺粰澶囩敤鐖讹紱
  澶辫触鍗宠浆鎶曚笅涓€涓埗鑺傜偣銆傚疄娴嬶細鎶婃煇涓腑缁х殑涓婅鍘嬪埌 15 kbps 鎸佺画 100s锛?  鍏跺瓙鑺傜偣闈犲鐢ㄧ埗琛ラ綈锛屽叏绋嬫病鏈夊崱椤裤€?- **闂ㄦ帶涓庡崱椤跨瓥鐣?*锛圫PEC 搂7.3銆伮?.6锛夛細璧锋挱/鎹㈢埗鍚庢寜"涓绘挱鏃堕棿鎴虫墍鍦ㄥ垎鐗?+ 杩炵画 n 鐗?寮€闂革紙n=4 / 閲嶇亴 n=2锛屾棤瓒呮椂锛夛紱
  鍗￠】涓婃姤 `stallCount` 鈫?鏈嶅姟绔妸褰撳墠涓荤埗鏍囦负閬垮紑骞剁珛鍒婚噸绠楄矾寰勶紙3s 闄愭祦锛夆啋 鏂拌矾寰勫埌浣嶅悗閲嶆柊寮€闂?鈫?  鍒嗙骇鍔犻€熻拷璧讹紙涓婇檺 +25%锛夆啋 婊炲悗 >12s 鎻愮ず骞惰烦鍒颁富鎾繘搴?鈫?鐩爣鍒嗙墖 2.5s 鍙栦笉鍒板氨鏀规寜涓绘挱褰撳墠杩涘害鍙栫墖銆?- **鐪熸満楠岃瘉**锛坄test/script/verify-m3.mjs`锛? 涓嫭绔?Chrome锛岄摼寮忔爲 host鈫?鈫?鈫?锛夛細
  - 楠屾敹 A锛氭繁搴?`[0,1,2,3]`锛屼竴绾ц妭鐐圭湡鐨勫湪缁欎笅绾т緵鐗囷紝鍏ㄥ憳鎾斁锛?  - 楠屾敹 B锛氭敞鍏ヤ富鎾笂琛屽悗杩涘叆**鍗曢摼妯″紡**锛屼富鎾彧鏈?1 涓瓙鑺傜偣锛堝垎鍙戣妭鐐癸級锛屽叾浣欏叏鎸傚湪瀹冧笅闈紱
  - 楠屾敹 C锛氫腑缁ф椂閽熶袱绾х浉鍔犺嚜娲斤紙`offset = hop + parent`锛夛紝娣卞害 鈮? 鐨勮妭鐐圭‘瀹炴敹鍒板甫涓户鎴崇殑鏍锋湰锛?  - 楠屾敹 D锛氬紑闂告椂鍒荤殑杩炵画鍒嗙墖鏁?鈮?闃堝€硷紙27/4銆?2/2銆?1/4 鐗囷級銆?
### 鏈嶅姟绔垏鐗囨湇鍔★紙M4锛?
娌℃湁 ffmpeg 鐨勭敤鎴峰彲浠ユ妸婧愯棰?*涓婁紶鍒版湇鍔″櫒**鍋氫竴娆℃€у垏鐗囷紝鍐嶄竴娆℃€т笅杞藉洖鏈湴锛?鐩存挱閾捐矾涓婄殑瑙嗛瀛楄妭浠嶇劧鍙蛋 P2P锛屾湇鍔″櫒涓嶅湪鍒嗗彂璺緞涓娿€?
```bash
# 涓婁紶骞跺垏鐗囷紙multipart 瀛楁鍚嶅浐瀹氫负 file锛?curl -F "file=@movie.mp4" http://127.0.0.1:8080/api/v1/segment/jobs
# 鈫?{"jobId":"...","state":"queued","queuePosition":0}

# 鏌ョ姸鎬侊紱done 涔嬪悗 result 閲屽甫鍒嗙墖鏁颁笌浜や粯褰㈡€?curl http://127.0.0.1:8080/api/v1/segment/jobs/<jobId>
# 鈮?GiB锛氬崟娆℃祦寮?zip锛?1GiB锛欽SON manifest锛堝惈 parts 鍒楄〃锛?curl -o out.zip http://127.0.0.1:8080/api/v1/segment/jobs/<jobId>/result
curl -o part1.zip http://127.0.0.1:8080/api/v1/segment/jobs/<jobId>/parts/1
```

閰嶉锛氬苟鍙?2銆佹帓闃?8銆佷护鐗屾《 3 浣滀笟/鍒嗛挓锛堣秴闄?429 + `Retry-After`锛夈€佸崟浣滀笟 鈮?0 鍒嗛挓銆佹簮鏂囦欢 鈮?6GiB銆?浜х墿淇濈暀 30 鍒嗛挓锛圱TL 娓呯悊锛夈€傚叏閮ㄥ彲鐢?`PR_SEGMENT_*` 瑕嗙洊锛宖fmpeg 璺緞鐢?`PR_FFMPEG`銆?涓嶆兂涓婁紶涔熻锛歚docs/SEGMENT.md` 閲屾湁鏈湴 ffmpeg / `cmd/segmenter` 鐨勫畬鏁存暀绋嬨€?
涓绘挱椤垫湁瀵瑰簲鍏ュ彛锛氶€夌墖鍖轰笅鏂圭殑銆?*鏈満娌℃湁 ffmpeg锛熶氦缁欐湇鍔″櫒鍒囩墖**銆嶅睍寮€鍚庡彲涓婁紶瑙嗛銆?鐪嬫帓闃熶綅娆′笌鍒囩墖杩涘害锛屽垏瀹屽彲浠ヤ笅杞?zip锛屾垨鑰呯洿鎺?*鍐欒繘涓€涓湰鍦扮洰褰曞苟绔嬪嵆寮€鎾?*
锛坄showDirectoryPicker()` + File System Access API锛涙祻瑙堝櫒涓嶆敮鎸佹椂閫€鍖栦负"涓嬭浇 zip 鎵嬪姩瑙ｅ帇鍚庡啀閫夌洰褰?锛夈€?鏈嶅姟鍣ㄨ繛涓嶄笂鎴栨病瑁?ffmpeg 鏃讹紝闈㈡澘浼氱洿鎺ヨ鏄庡師鍥狅紝鍐呭祵鐨勬湰鍦板垏鐗囨暀绋嬪缁堝彲鐢ㄣ€?
```bash
# 鐣岄潰楠屾敹锛氱湡瀹?Chrome 閲屽睍寮€鍏ュ彛銆佺‘璁ら潰鏉?鎺㈡祴寰芥爣/鏁欑▼锛屽苟鐣欎竴寮犳埅鍥?node test/script/verify-segment-ui.mjs
```

### M2 宸蹭氦浠?
- **`cmd/segmenter`**锛氭妸鏈湴瑙嗛鍒囨垚 `init.mp4` + `c00001.m4s鈥 + `index.json`銆?  鏀寔 `-fragment`锛堟棤鎹熼噸鏂板皝瑁咃級涓?`-transcode 1200k`锛堜綆涓婅棰勮锛夛紝骞剁洿鎺ユ墦鍗?  "杩欎釜鐮佺巼涓嬩富鎾兘甯﹀嚑涓汉"銆?- **鍒囩墖姝ｇ‘鎬х殑鍒ゆ嵁**锛歩nit + 鍏ㄩ儴鍒嗙墖鎸夊簭鎷兼帴蹇呴』涓庡師鏂囦欢**閫愬瓧鑺傜浉鍚?*锛?  涓旀瘡涓垎鐗囬兘浠?`moof` 寮€澶淬€傛寜鍥哄畾瀛楄妭鏁板垏鍒嗕細浜х敓"鍗婁釜 moof"锛屾祻瑙堝櫒浼氱洿鎺ユ姏閿欍€?- **鍓嶇鎾斁閾捐矾**锛歚useMediaIndex`锛堥€夌墖 + `isTypeSupported` 纭牎楠岋級鈫?  `useWebRTC`锛堟槦褰㈢洿杩?+ 棰勫崗鍟?DataChannel + `getStats` 涓婅浼扮畻锛夆啋
  `useChunkRequester`锛堜簩杩涘埗甯?+ 瓒呮椂锛夆啋 `useChunkPlayer`锛圡ediaSource锛夆啋
  `useSyncClock`锛圢TP 鏈€灏忔护娉?+ 涓夌骇婕傜Щ鐭锛夈€?- **瀹归噺闂搁棬**锛氭嬁鍒板疄娴嬩笂琛屽墠 `mode=pending`锛涗箣鍚庢寜 `1+K0` 鎷掔粷瓒呴瑙備紬锛屼笖鍙嫤鏂板姞鍏ャ€佷笉韪汉銆?
### 瀹炴祴鏁版嵁锛堢湡瀹?Chrome锛屼袱涓嫭绔嬫祻瑙堝櫒瀹炰緥锛?
| 鎸囨爣 | 瀹炴祴 | SPEC 鐩爣 |
| :--- | :--- | :--- |
| 瑙備紬璧锋挱鑰楁椂 | **0.4s** | 1鈥?s |
| 鍚屾鍋忓樊 max / p95 | **190ms / 130ms** | < 500ms |
| 鎴夸富鏆傚仠 鈫?瑙備紬璺熼殢 | 鏄?| 鏄?|
| 鎴夸富璺宠浆 鈫?瑙備紬璺熼殢 | 璺宠浆鍚庝綅缃竴鑷达紙鍋忓樊 0s锛?| 璺熼殢 |
| 鍒嗙墖浜や粯 | 74 娆★紝0 瓒呮椂锛? 澶辫触 | 鈥?|

澶嶇幇鏂瑰紡瑙佷笅鏂广€岄獙鏀躲€嶃€?
---

## 蹇€熷紑濮?
鍓嶇疆锛欸o 1.26+銆丯ode 20+銆乣ffmpeg`锛堢敤浜庢妸鏈湴瑙嗛棰勫鐞嗘垚 fMP4 鍒嗙墖锛夈€?
```bash
# 缁堢 1锛氭湇鍔＄锛堥粯璁?127.0.0.1:8080锛?go run ./cmd

# 缁堢 2锛氬墠绔紙榛樿 127.0.0.1:5173锛?api 涓?/ws 鑷姩浠ｇ悊鍒版湇鍔＄锛?cd client && npm install && npm run dev
```

娴忚鍣ㄦ墦寮€ http://127.0.0.1:5173 锛屼竴涓獥鍙ｃ€屽垱寤烘埧闂淬€嶅綋涓绘挱锛屽彟涓€涓獥鍙ｇ敤鎴块棿鐮併€屽姞鍏ユ埧闂淬€嶃€?
### 涓绘挱鍑嗗瑙嗛

```bash
# 鏅€?MP4 鈫?鍒嗙墖鐩綍锛堟棤鎹熼噸鏂板皝瑁咃紝涓嶉噸鏂扮紪鐮侊級
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -fragment -frag-sec 2

# 涓婅涓嶈冻鏃剁殑浣庣爜鐜囬璁撅紙閲嶆柊缂栫爜锛岃€楁椂闅忕墖闀垮闀匡級
go run ./cmd/segmenter -in movie.mp4 -out ./room-media -transcode 1200k
```

鐒跺悗鍦ㄤ富鎾帶鍒跺彴鐐广€岄€夋嫨鍒嗙墖鐩綍銆嶏紝閫変腑 `room-media/` 鍗冲彲寮€鎾€?
`segmenter` 浼氭墦鍗板閲忔彁绀猴紝渚嬪锛?
```
瀹归噺鎻愮ず锛堟寜涓绘挱涓婅 12.0 Mbps 浼拌锛?
  K0 = 4 鈫?鎵囧嚭妯″紡锛氫富鎾彲鐩存帴鏈嶅姟 4 涓竴绾ц妭鐐癸紝鍏朵綑鎴愬憳鎸傚埌瀹冧滑涓嬮潰銆?```

### 閰嶇疆

鏈嶅姟绔叏閮ㄩ厤缃兘鏈夐潰鍚戞湰鏈哄紑鍙戠殑榛樿鍊硷紝鍙敤鐜鍙橀噺瑕嗙洊锛?
| 鍙橀噺 | 榛樿鍊?| 璇存槑 |
| :--- | :--- | :--- |
| `PR_ADDR` | `127.0.0.1:8080` | 鐩戝惉鍦板潃 |
| `PR_MAX_MEMBERS` | `16` | 鎴块棿鎴愬憳纭笂闄愶紙鐪熷疄涓婇檺鐢卞疄娴嬩笂琛岀畻鍑虹殑 `1+K0` 鍐冲畾锛?|
| `PR_DEFAULT_STREAM_BPS` | `2000000` | 灏氭湭鎷垮埌 mediaIndex 鏃剁殑鐮佺巼浼拌 |
| `PR_STUN_URLS` | `stun:stun.l.google.com:19302` | 閫楀彿鍒嗛殧 |
| `PR_TURN_URLS` / `PR_TURN_USER` / `PR_TURN_PASS` | 绌?| 鐩磋繛澶辫触鏃剁殑涓户锛堥粯璁や笉鍚敤锛?|

---

## 楠屾敹

### 鑷姩鍖栵紙鏈嶅姟绔?+ 鍓嶇鍗曟祴锛?
```bash
gofmt -l ./cmd ./internal          # 蹇呴』涓虹┖
go vet ./... && go vet -tags wireinject ./cmd
go test ./...                      # 8 涓寘

cd client && npm run typecheck && npm run build
```

### 绔炴€佹娴嬶紙-race锛?
Windows 涓?Go 鐨?`-race` 闇€瑕?gcc 鍏煎椹卞姩锛坢ingw-w64锛夛紝MSVC/clang 閮戒笉琛岋紱
鏈満娌℃湁 mingw 鏃剁敤瀹瑰櫒璺戯紙Docker Desktop 鍗冲彲锛屼笉鍔ㄤ富鏈猴級锛?
```bash
docker run --rm -v "D:/IT/program/go-program/ProjectionRoom:/app" -v pr-gomod:/go/pkg/mod \
  -w /app -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off -e GOTOOLCHAIN=local \
  golang:1.26 sh -c "go test -race ./..."
```

`GOPROXY` 蹇呴』鎸囧埌鑳介€氱殑婧愶紙瀹瑰櫒閲岀洿杩?`proxy.golang.org` 浼氳鎷掞級銆?鏈€鍚庝竴娆″叏缁匡細`config / handler / model / service/mp4 / service/segment / usecase` 鍏ㄩ儴 ok銆?瀹瑰櫒閲屾病鏈?ffmpeg锛屾濂介『甯﹁鐩?鏈嶅姟鍣ㄧ己 ffmpeg"杩欐潯鍒嗘敮銆?
### 鐪熷疄娴忚鍣ㄥ疄娴嬶紙M2 楠屾敹鑴氭湰锛?
闇€瑕?Go 鏈嶅姟绔笌 Vite dev server 閮藉湪杩愯锛屼笖鍑嗗涓€涓垎鐗囩洰褰曪細

```bash
node test/script/verify-m2.mjs --media ./room-media --duration 300

# M3锛氬灞傛爲 / 鍗曢摼鍒嗗彂锛堝彲娉ㄥ叆瀹炴祴涓婅锛屽洜涓?headless 鐨?getStats 涓嶄骇鐢熶及璁″€硷級
node test/script/verify-m3.mjs --media ./room-media --nodes 4 --duration 30
node test/script/verify-m3.mjs --media ./room-media --nodes 4 --host-uplink 800000 --uplink 1=6000000
```

鑴氭湰浼氬惎鍔?*涓や釜鐙珛鐨?Chrome 瀹炰緥**锛堜笉鑳芥槸鍚屼竴瀹炰緥鐨勪袱涓爣绛鹃〉锛氬悗鍙版爣绛句細琚喕缁?鑺傛祦锛?鏃細璁?CDP 璋冪敤鎸傛锛屼篃浼氭妸 200ms 鐨勫悓姝ュ惊鐜嫋鎴?1s锛岄偅娴嬬殑灏变笉鏄悓姝ョ簿搴︿簡锛夛紝
鍒嗗埆鎵紨涓绘挱涓庤浼楋紝鐒跺悗娴嬮噺璧锋挱鑰楁椂銆佸叏绋嬪亸宸垎甯冦€佹殏鍋?璺宠浆/缁х画鐨勮窡闅忔儏鍐碉紝
骞惰緭鍑?`PASS` / `FAIL`銆?
椤甸潰閫氳繃 `window.__pr.snapshot()` 鏆撮湶缁撴瀯鍖栫姸鎬佸揩鐓э紙`client/src/debug.ts`锛夛紝
鑴氭湰鍙蹇収銆佷笉鎶?DOM 鏂囨湰銆?
---

## 椤圭洰缁撴瀯

```
cmd/                    鍏ュ彛锛歮ain.go 鍙湁 鍔犺浇閰嶇疆 鈫?InitializeApp 鈫?Run
cmd/init.go             Init 鑱氬悎鏍逛笌 Run()锛坵ire 鍥句笌 main 鐨勫敮涓€浜ょ偣锛?cmd/wire.go             wire.Build 渚濊禆澹版槑锛坕nject 渚э紝鍕挎墜鍐?wire_gen.go锛?cmd/wire_gen.go         `go tool wire ./cmd` 鐢熸垚鐨勮閰嶄唬鐮侊紙鎻愪氦锛屼笉鎵嬫敼锛?cmd/segmenter/          瑙嗛鍒嗙墖宸ュ叿锛坢oof 杈圭晫鍒囩墖 + index.json锛?internal/config/        閰嶇疆缁撴瀯涓庡姞杞斤紙鐜鍙橀噺瑕嗙洊锛?internal/handler/       gin 璺敱銆?ws 澶勭悊涓庨敊璇槧灏勶紙HTTP/鍗忚閫傞厤灞傦級
internal/usecase/       涓氬姟鐢ㄤ緥锛氭埧闂寸敓鍛藉懆鏈熴€佹嫇鎵戝垎閰嶃€佹ā寮忓垽瀹氥€佹崲闃诧紙鍞竴鐘舵€佹潈濞侊級
internal/model/         鏁版嵁濂戠害锛氭秷鎭ā鍨嬨€佸垎鐗囩储寮曘€乸rotobuf 杞崲
internal/service/       WebSocket 淇′护灞傦紙Hub锛変笌 HTTP 鏈嶅姟鍣ㄧ敓鍛藉懆鏈?internal/service/mp4/   fragmented MP4 瑙ｆ瀽涓庢寜 moof 杈圭晫鍒囧垎
internal/utils/         涓庝笟鍔℃棤鍏崇殑灏忓伐鍏凤紙闅忔満鐮併€佸垏鐗囨瘮杈冿級
internal/pb/            protoc 鐢熸垚鐨?protobuf 浠ｇ爜
client/                 Vue 3 + TS + Vite 鍓嶇锛堟挱鏀鹃摼璺湪 src/composables/锛?test/script/verify-m2.mjs     鐪熷疄娴忚鍣ㄩ獙鏀惰剼鏈?docs/SPEC.md            璁捐鏂囨。锛堝崗璁€佹嫇鎵戙€佸悓姝ョ畻娉曘€侀噷绋嬬涓庨獙鏀舵爣鍑嗭級
```

**渚濊禆鏂瑰悜**锛歚handler 鈫?usecase 鈫?model`锛涘熀纭€璁炬柦锛坄service*`锛夊彧閫氳繃鎺ュ彛琚敤渚嬪紩鐢?锛坄wire.Bind(new(usecase.Broadcaster), new(*service.Hub))`锛夛紝`utils`/`model` 涓嶅弽鍚戜緷璧栦换浣曞眰銆?
**涓嶅彉閲?*锛歚internal/` 涓嬩换浣曚唬鐮侀兘涓嶅緱璇诲啓瑙嗛鏁版嵁锛涙湇鍔″櫒鐨勮亴璐ｈ竟鐣屽湪 SPEC 搂3 鏈夋槑纭〃鏍笺€?
---

## 寮€鍙戠害瀹?
- 娑堟伅瀛楁鍦?`internal/model/message.go` 涓?`client/src/types/protocol.ts` 鍚勬湁涓€浠斤紝
  鏀瑰姩蹇呴』鍚屾 鈥斺€?瀹冧滑鏄悓涓€涓崗璁殑涓や唤鎶曞奖锛涚嚎涓婄紪鐮佷负 protobuf
  锛坄proto/projection_room.proto` 鏄敮涓€婧愶紝杞崲鐐硅 `internal/model/pb_convert.go` 涓?`client/src/types/codec.ts`锛夈€?- 鍒嗙墖绱㈠紩鍦?`internal/model/index.go` 涓?`client/src/types/media.ts` 鍚岀悊銆?- 鍓嶇鏀瑰姩鏈熼棿寤鸿鍏堝仠鎺?Vite锛歐indows 涓婃枃浠跺啓鍏ョ殑鍘熷瓙鏇挎崲浼氫笌瀹冪殑 watcher 鎶㈤攣锛圗BUSY锛夈€?