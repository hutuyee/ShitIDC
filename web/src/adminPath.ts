// 后台路径。默认 /admin，可用 VITE_ADMIN_PATH 在构建时改掉（例如 /my-console）。
//
// 与后端 ADMIN_PATH 是两个独立的开关：
//   * 后端 ADMIN_PATH 决定 API 挂在哪个前缀（默认 /admin-panel，并总是镜像 /admin）
//   * 这个常量决定浏览器地址栏里的后台路径
// 之所以不强制两者一致：后端的 /admin 是兼容镜像，前端用 /admin 也照常能跑；
// 想彻底换掉的话两处都改即可。
//
// 注意：这是构建期常量（import.meta.env），改了要重新构建前端才生效。
export const ADMIN_PATH = (import.meta.env.VITE_ADMIN_PATH as string | undefined)?.replace(/\/+$/, '') || '/admin'
