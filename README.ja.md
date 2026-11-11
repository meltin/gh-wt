# gh-wt

`git worktree` を手軽に使うための GitHub CLI 拡張です。各 worktree と pull request の状態を一覧で確認できます。

GitHub の応答を待ちません。worktree は git が読み終えた時点ですぐに表示され、pull request は `gh` の応答が届いたところで埋まります。

[English](README.md)

## 使い方

``` console
$ gh wt                              # worktree を選んで移動する
$ gh wt -l                           # worktree を一覧表示する
$ gh wt <branch|worktree|path>       # worktree に移動する（なければ worktree とブランチを作る）
$ gh wt <branch> <start-point>       # <start-point> から新しいブランチを作り、worktree を作る
$ gh wt -b <branch> <worktree>       # ディレクトリ名と違う名前のブランチで worktree を作る
$ gh wt --orphan <branch|worktree>   # 履歴のない新しいブランチ（orphan ブランチ）で worktree を作る
$ gh wt -p                           # 開いている pull request を選び、worktree で開く
$ gh wt -p <number|url|branch>       # 指定した pull request を worktree で開く
$ gh wt -d <branch|worktree|path>... # worktree とブランチを削除する（安全）
$ gh wt -D <branch|worktree|path>... # worktree とブランチを強制的に削除する
$ gh wt -m [<old>] <new>             # worktree のディレクトリとブランチの名前を変える（安全）
$ gh wt -M [<old>] <new>             # 強制的に名前を変える（既存のブランチを上書きし、ロックされた worktree も動かす）
```

対象は次の 3 通りで指定できます。
- **branch**: git のブランチ名。例: `gh wt feature-branch`
- **worktree**: [`wt.basedir`](#wtbasedir)（既定は `.wt`）からの相対のディレクトリ名。例: `gh wt some-worktree-folder-name`
- **path**: 既存の worktree へのパス（絶対パス、または現在のディレクトリからの相対パス）。例: `gh wt ../sibling`、`gh wt /absolute/path`

この順に照合します。まずブランチ、次に `wt.basedir` の下のディレクトリ、最後にパスです。

> [!NOTE]
> gh-wt にサブコマンドはありません。フラグでない最初の引数は、常に対象の名前として扱います。そのため `gh wt list` は一覧を出さず、`list` という worktree を作ります。一覧を見るときは `gh wt -l` を使ってください。

gh-wt は [k1LoW/git-wt](https://github.com/k1LoW/git-wt) にならっています。対象の指定方法、`wt.*` の git config キー（[未対応のもの](#未対応の設定)を除く）、gh-wt にある操作の挙動は git-wt と同じで、`git` と `gh` だけで動きます。git や gh にすでにある機能は、それをそのまま実行し、出力もそのまま表示します。

### worktree の一覧

`gh wt` は現在の worktree 以外を一覧にして、選んだ worktree に移動します。`gh wt -l` は、すべての worktree を同じ表で出力します。

![gh wt で worktree を選ぶ](docs/demo/select.gif)

| 列          | 内容                                                                                                  |
| ----------- | ----------------------------------------------------------------------------------------------------- |
| BRANCH      | ブランチ名（最大 26 セル）、または `(detached)`                                                       |
| WT          | upstream より `↑` 進んでいる / `↓` 遅れているコミット数、`!` 衝突、`+` ステージ済み、`~` 変更、`?` 未追跡 |
| PR          | そのブランチの PR の番号と状態（open / draft / merged / closed）。開いている PR を優先                 |
| LAST COMMIT | 最後のコミットの件名と、どれくらい前か                                                                      |
| PATH        | worktree のパス。ホームは `~` で表示                                                                  |

- 表は、git が worktree を読み終えた時点ですぐに出ます。PR 列は `gh` の応答が届くまでスピナーを表示し、届いたら他の列を動かさずに埋まります。その間もカーソルを動かしたり選んだりできます。パイプに出すときの `gh wt -l` は、応答を待ってから一度に出力します。
- 開いている PR があるブランチでは、PR に push されたのに worktree にまだないコミットも、fetch せずに `↓` に数えます。`↓N` は、そうしたコミットがあるものの、まだ fetch していないので数えられないことを表します。
- 状態を読み取れない worktree では、WT が `unknown` になります（ロックされていて、マウントされていないボリュームにある worktree など）。
- 選択画面では、現在の worktree がヘッダーの下に灰色で表示され、選べません。
- 表が端末より広いときは、コミットの件名と BRANCH を短くします。PATH は、親ディレクトリを頭文字にした形（`~/d/w/r/feat-x`）にし、それでも入らなければ末尾を 20 セルまで切り、20 セルでも入らなければ列ごと省きます。さらに狭いときは、コミットの時刻、件名の順に省きます。

アイコンには [Nerd Fonts](https://www.nerdfonts.com/) を使っています。端末のフォントを Nerd Font にしてください。

### worktree の作成

一致する worktree がないとき、`gh wt <branch>` は `wt.basedir` の下で `git worktree add` を実行します。ローカルブランチがあればそれを、同じ名前のリモートブランチがあれば追跡付きでそれをチェックアウトします。どちらもなければ、`HEAD` から新しいブランチを作ります。

`HEAD` 以外から新しいブランチを作るときは、開始点を指定します。

``` console
$ gh wt fix/y main
```

リモートにしかない開始点は `<remote>/<start-point>` として使います（複数のリモートにあるときは `checkout.defaultRemote` で選びます）。そのため、新しいブランチは指定した名前のままになります。開始点は新しいブランチにだけ使えます。worktree やローカルブランチがすでにあるときは拒否します。

ブランチ名を worktree のディレクトリ名と変えたいときは、`-b`/`--branch` を使います。

``` console
$ gh wt -b user/my-feature my-feature
```

あとからは、ブランチ名とディレクトリ名のどちらでも移動できます。

``` console
$ gh wt user/my-feature  # ブランチ名で移動
$ gh wt my-feature       # ディレクトリ名で移動
```

`--orphan` を使うと、履歴のない新しいブランチで、空の worktree を作ります。

``` console
$ gh wt --orphan gh-pages
```

- `-b` とは組み合わせられますが、開始点とは組み合わせられません。ローカルブランチがすでにあるときは、git が拒否します。
- 空の状態で始められるように、ファイルはコピーしません。hook は実行します。

フラグは名前の前にも後ろにも置けます。`-b` は `--branch=NAME` や `-bNAME` の形でも受け付け、`--` 以降はフラグとして扱いません。

### pull request を開く

`gh wt -p` は、リポジトリで開いている pull request（フォークからのものも含む）を一覧にして、選んだものを worktree で開きます。`gh wt <branch>` と同じく、worktree があればそこへ移動し、なければ作ります。現在の worktree は、PR があればその PR とともにヘッダーの下に灰色で表示され、選べません。`gh wt -p <number>` で現在の worktree の PR を指定すると、その worktree を PR の最新に更新します。

![gh wt -p で pull request を開く](docs/demo/pr.gif)

``` console
$ gh wt -p          # 一覧から選ぶ
$ gh wt -p 1720     # #1720、PR の URL、ブランチ名でも指定できる
```

| 列       | 内容                                                                         |
| -------- | ---------------------------------------------------------------------------- |
| WT       | worktree があればその状態。`gh wt` の WT と同じ（選ぶとそこへ移動）          |
| PR       | 番号。open は緑、draft は黄色の `draft` 付き                                 |
| TITLE    | PR のタイトル。表が端末より広いときは、ここから短くする                      |
| BRANCH   | PR のブランチ                                                                |
| AUTHOR   | PR の作成者                                                                  |
| UPDATED  | PR が最後に更新された時刻                                                    |
| PATH     | worktree があればそのパス。ホームは `~` で表示                               |

PATH は worktree の一覧と同じく短くし、入らなければ省きます。さらに狭いときは、TITLE が 20 セルより短くなる前に、UPDATED、AUTHOR の順に省きます。

チェックアウトは `gh pr checkout --worktree` で行うので、PR は gh で開いたときと同じ状態で開きます。

- 先に PR を fetch します。普通の `gh wt <branch>` は fetch しません。
- 新しいブランチは PR のブランチを追跡します。既存のブランチは fast-forward します（できないときは拒否します）。
- フォークのブランチが既定ブランチと同じ名前のときは、gh と同じく `<owner>/<branch>` という名前になります。その PR のものではないローカルブランチと同じ名前のときも同様です。gh が手元のブランチをその PR まで fast-forward してしまうのを避けるためです。
- 別のリポジトリの PR の URL を渡すと、`gh pr checkout <url>` と同じく、その PR を fetch します。

gh-wt が加えるのは、`wt.basedir` の下のパスを決めることと、新しい worktree でのファイルのコピーと `wt.hook` の実行だけです。

### worktree の削除

削除でも同じ指定方法が使えます。例: `gh wt -d feature-branch`、`gh wt -d .`、`gh wt -d ../sibling`。対象は複数まとめて渡せます。失敗した対象があると、そこで処理を止めます。

`-d` は安全な削除で、何かが失われる場合は途中で止まります。

- worktree に変更されたファイルや未追跡のファイルがあるときは、何も削除しません。`node_modules` のような ignore されたファイルは数えません。
- ブランチが完全にはマージされていないときは、worktree だけを削除してブランチは残します。理由は git が表示しますが、gh-wt は `0` で終了します。終了コードだけでなく、メッセージを読んでください。

`-D` は両方の確認を飛ばし、worktree とブランチをまとめて削除します。

- worktree がなくローカルブランチだけがある対象は、ブランチだけを削除します（保護されたブランチは除きます。下の NOTE を参照）。
- ディレクトリがすでに消えている worktree も片付けます。
- `wt.basedir` の下で空になった親ディレクトリ（例: `feat/x` を消したあとの `feat/`）も削除します。
- 現在の worktree を削除したときは、シェル連携がメインのリポジトリへ移動します。

### worktree の名前の変更

`-m`（強制するときは `-M`）で、worktree のディレクトリとブランチの名前を一度に変えます。引数が 1 つなら現在の worktree を、2 つなら指定した worktree を対象にします。

``` console
$ gh wt -m new-name                 # 現在の worktree を "new-name" にする
$ gh wt -m old-name new-name        # worktree "old-name" を "new-name" にする
```

- ディレクトリは `<wt.basedir>/<new-name>` に移ります。
- 先にブランチの名前を変えます。不正な名前や衝突する名前は、ディスク上の何かを変える前に git が拒否します。そのあとディレクトリの移動に失敗したときは、ブランチの名前を元に戻します。
- 移動先がすでにあるときや、移動先が別の worktree の中になるときは、名前を変えません。
- `-M` は、新しい名前の既存ブランチを上書きし、ロックされた worktree も移動します。
- `wt.basedir` の下で空になった親ディレクトリ（例: `feat/x` の名前を変えたあとの `feat/`）は削除します。

現在の worktree の名前をシェル連携から変えたときは、シェル関数が新しいパスへ `cd` します（`-d` で現在の worktree を削除したときに、メインのリポジトリへ戻るのと同じ仕組みです）。

> [!NOTE]
> 既定ブランチと、メインのリポジトリの `HEAD` が指すブランチは保護されています。
> - `-d`/`-D` は、それらの worktree は削除しますが、ブランチは残します。worktree のないブランチを指定したときは拒否します。
> - `-m`/`-M` は、それらの名前を変えません。
> - メインの作業ツリーと、detached の worktree も名前を変えられません。
>
> 既定ブランチは `origin/HEAD` です。なければ `init.defaultBranch`、それもなければ `master` です。

## インストール

[GitHub CLI](https://cli.github.com/) をインストールしてログインしておくことと、端末のフォントを [Nerd Font](https://www.nerdfonts.com/) にすることが必要です。

gh-wt は macOS と Linux 向けです。Windows 用のバイナリも配布していますが、動作は確認していません。ファイルのコピー（`wt.copy*`）は `cp` を、フックは `sh` を呼び、シェル関数は zsh 用です。そのため、WSL や Git Bash のように、これらが使える環境が必要です。

``` console
$ gh extension install meltin/gh-wt
```

## シェル連携

拡張は子プロセスとして動くので、シェルのディレクトリを変えられません。worktree へ移動できるように、`~/.zshrc` に次の 1 行を足してください。

``` zsh
eval "$(gh wt --init zsh)"
```

> [!IMPORTANT]
> シェル連携は、`gh wt <branch>` で自動的にディレクトリを移動するために、`gh()` というラッパー関数を作ります。このラッパーが受け持つのは `gh wt` だけで、ほかの gh コマンドはそのまま通します。`gh` コマンドを包む別のツールや設定があるときは、ぶつかることがあります。

`cd` するのはこのラッパー関数なので、上の 1 行を実行したシェルの中でしか移動しません。拡張自体は、移動先の worktree のパスを、必ず**標準出力の最後の行**に出力します。ラッパーはこの行を読んでいます。git と gh の出力や、[hook](#wthook) の出力はすべて標準エラー出力に出るので、スクリプトやエディタなどのツールも最後の行を当てにできます。

``` console
$ WT=$(gh wt feature-branch | tail -1)
$ git -C "$WT" status
```

- 終了コードはそのまま返します。worktree を作ったあとに hook が失敗したときも、worktree へは移動し、終了コードは 0 以外になります。
- `gh wt -l`、`gh wt -h`/`--help`、`gh wt --init` はラッパーを通さずに実行するので、色や端末の幅がそのまま使われます。
- この 1 行は、`gh` に PATH が通ったあとに置いてください。それより前に定義した `gh` 関数は置き換えます。`gh` のエイリアスがあっても、`setopt nounset` でも動きます。
- gh-wt を更新すると、次にシェルを開いたときに関数も新しくなります。中身は `gh wt --init zsh` で確認できます。

## 設定

設定は `git config` で行います。キーは git-wt と同じなので、git-wt の設定がそのまま使えます。

#### `wt.basedir`

worktree を置くディレクトリです。

``` console
$ git config wt.basedir "../{gitroot}-worktrees"
```

使えるテンプレート変数:
- `{gitroot}`: リポジトリのルートディレクトリの名前

相対パスはリポジトリからの相対になり、`~` はホームディレクトリを表します。

既定値: `.wt`

#### `wt.copyignored`

`.gitignore` で無視されているファイル（例: `.env`）を、新しい worktree にコピーします。

``` console
$ git config wt.copyignored true
```

既定値: `false`

#### `wt.copyuntracked`

未追跡のファイル（まだ git に追加していないファイル）を、新しい worktree にコピーします。

``` console
$ git config wt.copyuntracked true
```

既定値: `false`

#### `wt.copymodified`

変更されたファイル（追跡されていて、コミットしていない変更があるファイル）を、新しい worktree にコピーします。

``` console
$ git config wt.copymodified true
```

既定値: `false`

> [!NOTE]
> - コピー元は、gh-wt を実行した worktree です。worktree を置くディレクトリ（`wt.basedir`）は、常にコピーの対象から外します。
> - macOS では `cp -c`（APFS のコピーオンライト）でファイルを複製するので、`node_modules` のような大きなディレクトリもすぐにコピーできます。

#### `wt.hook`

新しい worktree を作ったあとに実行するコマンドです。新しい worktree のディレクトリで実行します。

``` console
$ git config --add wt.hook "npm install"
$ git config --add wt.hook "go generate ./..."
```

> [!NOTE]
> - hook は、新しい worktree を**作ったとき**だけ実行します。既存の worktree に移動するときは実行しません。
> - hook が失敗すると、残りの hook は実行せず、`gh wt` はエラーで終了します。その時点で worktree はできているので、シェル連携はそこへ `cd` し、0 以外の終了コードはスクリプトからも見えます。
> - hook は `sh -c` で実行します。失敗してもかまわない hook は、`git config --add wt.hook "direnv allow || true"` のように書けます。

#### 未対応の設定

`wt.copy`、`wt.nocopy`、`wt.symlink`、`wt.deletehook`、`wt.remover`、`wt.nocd`、`wt.relative`、git-wt の実行ごとのフラグ（`--basedir`、`--copyignored` など）には、まだ対応していません（[#3](https://github.com/meltin/gh-wt/issues/3)）。
