# Theme SDK（主题开发规范）

主题与业务代码完全分离（第九/二十阶段）：一个主题就是 **静态资源包**，通过 CSS 变量契约换肤，永远接触不到数据库、系统命令或服务端逻辑。

## 1. 包结构

```text
themes/
└── mytheme/
    ├── theme.json        # 清单（必需）
    ├── variables.css     # CSS 变量契约（必需）
    ├── preview.webp      # 后台预览图（可选）
    └── assets/           # 静态资源（可选）
        └── hero.png
```

## 2. theme.json

```json
{
  "id": "mytheme",
  "name": "My Theme",
  "version": "1.0.0",
  "author": "you",
  "description": "一句话介绍",
  "engine": "1"
}
```

- `id`：仅允许字母、数字、`-`、`_`，将作为目录名与访问路径（`/themes/mytheme/variables.css`）。
- `engine`：`"1"` 表示遵循本文 CSS 变量契约。

## 3. variables.css 契约

必须提供以下变量（可参考 `themes/default/variables.css`）：

```css
:root {
  --primary: #4f46e5;      /* 品牌主色 */
  --background: #ffffff;   /* 页面背景 */
  --panel: #f6f7fb;        /* 卡片/面板背景 */
  --text: #1c2333;         /* 正文文字 */
  --muted: #8a93a6;        /* 次要文字 */
  --border: #e5e8f0;       /* 分割线 */
  --radius: 12px;          /* 圆角 */
}
```

前台通过 `/themes/{id}/variables.css` 加载（Go 静态服务），切换主题只替换 `<link href>`，并同步 Naive UI 的暗色组件主题。

## 4. 打包上传

把目录内容打成 zip（`theme.json` 必须在 zip 根）：

```bash
cd themes/mytheme && zip -r ../mytheme.zip .
```

管理后台 → 扩展 / 主题 → 上传主题 zip → 激活。上传时服务端强制校验：

- **Zip Slip**：任何 `../`、绝对路径、反斜杠路径直接拒绝（§44）
- **压缩炸弹**：解压后总大小 / 文件数上限
- **文件类型白名单**：仅 css / js / json / 图片 / 字体 / 文本，禁止任何可执行内容（§20）
- 暗色主题：在同一定义里覆盖变量即可（参考 `themes/dark`）
