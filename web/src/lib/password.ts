import { api } from './api'

/** Same rule as the server: at least 12 characters (counted as characters, not bytes). */
export const MIN_PASSWORD_LENGTH = 12

/**
 * A password change removes every push subscription of the account, because
 * subscriptions are bound to the user rather than to one login. Re-register
 * this device's existing subscription so only the other devices stop
 * receiving notifications. Returns 'none' when this device had no
 * subscription, 'restored' on success and 'failed' otherwise; never throws.
 */
export async function resubscribeThisDevice(): Promise<'none' | 'restored' | 'failed'> {
  try {
    const registration = await navigator.serviceWorker?.getRegistration()
    const subscription = await registration?.pushManager?.getSubscription()
    if (!subscription) return 'none'
    await api.subscribePush(subscription.toJSON())
    return 'restored'
  } catch {
    return 'failed'
  }
}
