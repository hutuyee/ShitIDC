const configuredSiteName = import.meta.env.VITE_SITE_NAME?.trim()

export const siteName: string = configuredSiteName || 'ShitIDC'
export const siteLogoUrl: string = import.meta.env.VITE_SITE_LOGO_URL?.trim() || ''

export function siteInitial(): string {
  const first: string = [...siteName][0] || 'S'
  return first.toUpperCase()
}
