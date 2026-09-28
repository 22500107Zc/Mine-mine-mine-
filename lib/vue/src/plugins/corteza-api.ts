import { apiClients } from '@cortezaproject/corteza-js'
import { PluginFunction } from 'vue'

interface Options {
  baseURL?: string;
  accessTokenFn?: () => string | undefined;
}

/**
 * Generic Corteza API plugin
 *
 * Install a specific plugin:
 * Vue.use(plugins.CortezaAPI('compose'))
 *
 * @constructor
 */
export default function (service: string, opt: Options = {}): PluginFunction<Options> {
  if (!opt.baseURL) {
    // @ts-ignore
    if (!window.CortezaAPI) {
      throw new Error('config.js missing or window.CortezaAPI not set')
    }

    // @ts-ignore
    opt.baseURL = `${window.CortezaAPI}/${service}`
  }

  return function (Vue): void {
    service = service.substring(0, 1).toUpperCase() + service.substring(1)

    if (!opt.accessTokenFn) {
      /**
       * Checking if auth plugin was initialized before and
       * hooking on to it's accessTokenFn
       */
      opt.accessTokenFn = Vue.prototype.$auth.accessTokenFn
    }

    // @ts-ignore
    const client = new apiClients[service](opt)
    const api = client.api.bind(client)
    client.api = () => {
      const instance = api()
      instance.interceptors.response.use(undefined, accessGuard)
      return instance
    }

    // makes Vue.$<service>API (Vue.$SystemAPI, Vue.$ComposeAPI, Vue.$FederationAPI, Vue.$AutomationAPI) available
    Vue.prototype[`$${service}API`] = client
  }
}

/**
 * Sends the user to the account page when the server refuses a request
 * because the company was disabled or its subscription is not active
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function accessGuard (error: any): Promise<never> {
  const access = (((error || {}).response || {}).headers || {})['x-culpos-access']
  const target = access === 'disabled' ? '/account/disabled' : access === 'billing' ? '/billing' : ''

  if (target && window.location.pathname !== target) {
    window.location.assign(target)
  }

  return Promise.reject(error)
}
