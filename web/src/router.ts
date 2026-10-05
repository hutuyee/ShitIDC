import { createRouter, createWebHistory } from 'vue-router'
import Login from './views/Login.vue'
import Dashboard from './views/Dashboard.vue'
import Announcements from './views/Announcements.vue'
import AnnouncementDetail from './views/AnnouncementDetail.vue'
import Products from './views/Products.vue'
import Orders from './views/Orders.vue'
import Invoices from './views/Invoices.vue'
import InvoiceCenter from './views/InvoiceCenter.vue'
import AdminInvoices from './views/AdminInvoices.vue'
import AdminEmailNotice from './views/AdminEmailNotice.vue'
import Cart from './views/Cart.vue'
import Wallet from './views/Wallet.vue'
import Vouchers from './views/Vouchers.vue'
import Services from './views/Services.vue'
import FlowPackets from './views/FlowPackets.vue'
import Messages from './views/Messages.vue'
import Tickets from './views/Tickets.vue'
import TicketDetail from './views/TicketDetail.vue'
import Profile from './views/Profile.vue'
import ApiTokens from './views/ApiTokens.vue'
import Admin from './views/Admin.vue'
import AdminProviders from './views/AdminProviders.vue'
import AdminServices from './views/AdminServices.vue'
import AdminServiceTransfers from './views/AdminServiceTransfers.vue'
import AdminFlowPackets from './views/AdminFlowPackets.vue'
import AdminClientCare from './views/AdminClientCare.vue'
import AdminTicketInternal from './views/AdminTicketInternal.vue'
import AdminTicketInternalDetail from './views/AdminTicketInternalDetail.vue'
import AdminTicketInternalSettings from './views/AdminTicketInternalSettings.vue'
import AdminTicketInternalCron from './views/AdminTicketInternalCron.vue'
import AdminTicketInternalStats from './views/AdminTicketInternalStats.vue'
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
import AdminVouchers from './views/AdminVouchers.vue'
import AdminPromotions from './views/AdminPromotions.vue'
import AdminCycleOrders from './views/AdminCycleOrders.vue'
import AdminCashbacks from './views/AdminCashbacks.vue'
import AdminOrderCosts from './views/AdminOrderCosts.vue'
import AdminInspectionRecords from './views/AdminInspectionRecords.vue'
import AdminCertifications from './views/AdminCertifications.vue'
import AdminClientFields from './views/AdminClientFields.vue'
import AdminProductLimits from './views/AdminProductLimits.vue'
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
  { path: '/invoice', component: InvoiceCenter },
  { path: '/wallet', component: Wallet },
  { path: '/vouchers', component: Vouchers },
  { path: '/services', component: Services },
  { path: '/flow-packets', component: FlowPackets },
  { path: '/messages', component: Messages },
  { path: '/messages/:id', component: Messages },
  { path: '/tickets', component: Tickets },
  { path: '/tickets/:id', component: TicketDetail },
  { path: '/profile', component: Profile },
  { path: '/tokens', component: ApiTokens },
  { path: ADMIN_PATH + '', component: Admin },
  { path: ADMIN_PATH + '/orders', component: AdminOrders },
  { path: ADMIN_PATH + '/stats', component: AdminStats },
  { path: ADMIN_PATH + '/coupons', component: AdminCoupons },
  { path: ADMIN_PATH + '/vouchers', component: AdminVouchers },
  { path: ADMIN_PATH + '/promotions', component: AdminPromotions },
  { path: ADMIN_PATH + '/cycle-orders', component: AdminCycleOrders },
  { path: ADMIN_PATH + '/invoices', component: AdminInvoices },
  { path: ADMIN_PATH + '/email-notice', component: AdminEmailNotice },
  { path: ADMIN_PATH + '/cashbacks', component: AdminCashbacks },
  { path: ADMIN_PATH + '/order-costs', component: AdminOrderCosts },
  { path: ADMIN_PATH + '/inspection-records', component: AdminInspectionRecords },
  { path: ADMIN_PATH + '/certifications', component: AdminCertifications },
  { path: ADMIN_PATH + '/client-fields', component: AdminClientFields },
  { path: ADMIN_PATH + '/product-limits', component: AdminProductLimits },
  { path: ADMIN_PATH + '/export', component: AdminExport },
  { path: ADMIN_PATH + '/expired-ip-logs', component: AdminExpiredIpLogs },
  { path: ADMIN_PATH + '/agents', component: AdminAgents },
  { path: ADMIN_PATH + '/ext-themes', component: AdminExtThemes },
  { path: '/referral', component: Referral },
  { path: ADMIN_PATH + '/products', component: AdminProducts },
  { path: ADMIN_PATH + '/announcements', component: AdminAnnouncements },
  { path: ADMIN_PATH + '/providers', component: AdminProviders },
  { path: ADMIN_PATH + '/services', component: AdminServices },
  { path: ADMIN_PATH + '/service-transfers', component: AdminServiceTransfers },
  { path: ADMIN_PATH + '/flow-packets', component: AdminFlowPackets },
  { path: ADMIN_PATH + '/client-care', component: AdminClientCare },
  { path: ADMIN_PATH + '/ticket-internal', component: AdminTicketInternal },
  { path: ADMIN_PATH + '/ticket-internal/settings', component: AdminTicketInternalSettings },
  { path: ADMIN_PATH + '/ticket-internal/cron', component: AdminTicketInternalCron },
  { path: ADMIN_PATH + '/ticket-internal/stats', component: AdminTicketInternalStats },
  { path: ADMIN_PATH + '/ticket-internal/:id', component: AdminTicketInternalDetail },
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
