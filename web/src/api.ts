import axios from 'axios'

export const api = axios.create({ baseURL: '/api/v1', withCredentials: true, timeout: 15000 })
let csrf = localStorage.getItem('csrf_token') || ''
export function setCSRF(value: string) { csrf = value; if (value) localStorage.setItem('csrf_token', value); else localStorage.removeItem('csrf_token') }
api.interceptors.request.use(config => {
  const method = (config.method || 'get').toLowerCase()
  if (!['get','head','options'].includes(method) && csrf) config.headers['X-CSRF-Token'] = csrf
  return config
})
export function dataOf<T>(r: any): T { return r.data.data as T }
