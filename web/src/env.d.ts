/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_SITE_NAME?: string
  readonly VITE_SITE_LOGO_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
