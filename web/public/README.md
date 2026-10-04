# Branding assets

把你的站点头像 / Logo 放在这个目录，例如：

```text
web/public/logo.png
```

然后在根目录 `.env` 中设置：

```env
VITE_SITE_NAME=ShitIDC
VITE_SITE_LOGO_URL=/logo.png
```

如果 `VITE_SITE_LOGO_URL` 留空，前端会显示站点名称首字母作为默认占位头像。
