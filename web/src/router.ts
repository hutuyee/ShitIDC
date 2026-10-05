import { createRouter, createWebHistory } from 'vue-router'
import Login from './views/Login.vue'
import Dashboard from './views/Dashboard.vue'
import Announcements from './views/Announcements.vue'
import AnnouncementDetail from './views/AnnouncementDetail.vue'
import Products from './views/Products.vue'
import Orders from './views/Orders.vue'
import Invoices from './views/Invoices.vue'
import Cart from './views/Cart.vue'
import Wallet from './views/Wallet.vue'
import Services from './views/Services.vue'
import Tickets from './views/Tickets.vue'
import TicketDetail from './views/TicketDetail.vue'
import Profile from './views/Profile.vue'
import ApiTokens from './views/ApiTokens.vue'
import Admin from './views/Admin.vue'
import AdminProviders from './views/AdminProviders.vue'
import AdminServices from './views/AdminServices.vue'
import AdminUsers from './views/AdminUsers.vue'
import AdminTickets from './views/AdminTickets.vue'
import AdminLogs from './views/AdminLogs.vue'
import AdminSettings from './views/AdminSettings.vue'
import AdminPayments from './views/AdminPayments.vue'
import AdminWebhooks from './views/AdminWebhooks.vue'
import AdminOrders from './views/AdminOrders.vue'
import AdminProducts from './views/AdminProducts.vue'
import AdminAnnouncements from './views/AdminAnnouncements.vue'
import AdminStats from './views/AdminStats.vue'
import AdminCoupons from './views/AdminCoupons.vue'
import AdminCashbacks from './views/AdminCashbacks.vue'
import AdminCertifications from './views/AdminCertifications.vue'
import AdminExport from './views/AdminExport.vue'
import AdminExpiredIpLogs from './views/AdminExpiredIpLogs.vue'
import AdminAgents from './views/AdminAgents.vue'
import AdminExtThemes from './views/AdminExtThemes.vue'
import Referral from './views/Referral.vue'
import NotFound from './views/NotFound.vue'
import { useAuthStore } from './stores/auth'
import { ADMIN_PATH } from './adminPath'

const router = createRouter({ history: createWebHistory(), routes: [
  { path: '/login', component: Login, meta: { public: true } },
  { path: '/', component: Dashboard },
  { path: '/products', component: Products },
  { path: '/cart', component: Cart },
  { path: '/announcements', component: Announcements },
  { path: '/announcements/:id', component: AnnouncementDetail },
  { path: '/orders', component: Orders },
  { path: '/invoices', component: Invoices },
  { path: '/wallet', component: Wallet },
  { path: '/services', component: Services },
  { path: '/tickets', component: Tickets },
  { path: '/tickets/:id', component: TicketDetail },
  { path: '/profile', component: Profile },
  { path: '/tokens', component: ApiTokens },
  { path: ADMIN_PATH + '', component: Admin },
  { path: ADMIN_PATH + '/orders', component: AdminOrders },
  { path: ADMIN_PATH + '/stats', component: AdminStats },
  { path: ADMIN_PATH + '/coupons', component: AdminCoupons },
  { path: ADMIN_PATH + '/cashbacks', component: AdminCashbacks },
  { path: ADMIN_PATH + '/certifications', component: AdminCertifications },
  { path: ADMIN_PATH + '/export', component: AdminExport },
  { path: ADMIN_PATH + '/expired-ip-logs', component: AdminExpiredIpLogs },
  { path: ADMIN_PATH + '/agents', component: AdminAgents },
  { path: ADMIN_PATH + '/ext-themes', component: AdminExtThemes },
  { path: '/referral', component: Referral },
  { path: ADMIN_PATH + '/products', component: AdminProducts },
  { path: ADMIN_PATH + '/announcements', component: AdminAnnouncements },
  { path: ADMIN_PATH + '/providers', component: AdminProviders },
  { path: ADMIN_PATH + '/services', component: AdminServices },
  { path: ADMIN_PATH + '/users', component: AdminUsers },
  { path: ADMIN_PATH + '/tickets', component: AdminTickets },
  { path: ADMIN_PATH + '/logs', component: AdminLogs },
  { path: ADMIN_PATH + '/settings', component: AdminSettings },
  { path: ADMIN_PATH + '/payments', component: AdminPayments },
  { path: ADMIN_PATH + '/webhooks', component: AdminWebhooks },
  { path: '/:pathMatch(.*)*', component: NotFound, meta: { public: true } }
]})
router.beforeEach(async to => { const auth=useAuthStore(); if(!auth.ready) await auth.me(); if(!to.meta.public && !auth.user) return '/login'; if(to.path==='/login' && auth.user) return '/'; })
export default router
