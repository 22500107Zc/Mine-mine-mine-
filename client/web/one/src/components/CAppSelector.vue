<template>
  <div class="app-selector d-flex flex-column h-100 py-2">
    <div class="d-flex flex-column justify-content-center align-items-center mx-4 my-2">
      <b-img
        :src="logo"
        class="logo px-2"
      />

      <div class="search w-100 mx-auto my-4">
        <c-input-search
          v-model.trim="query"
          :aria-label="$t('search')"
          :placeholder="$t('search')"
          :debounce="200"
        />
      </div>
    </div>

    <div class="flex-fill overflow-auto">
      <b-container class="h-100">
        <draggable
          v-if="filteredApps.length"
          v-model="appList"
          :disabled="!canCreateApplication || query || isMobileResolution"
          group="apps"
          class="h-100 w-100"
          @end="onDrop"
        >
          <transition-group
            name="apps"
            tag="b-row"
            class="d-flex flex-wrap align-items-stretch justify-content-center mx-2"
          >
            <b-col
              v-for="app in filteredApps"
              :key="app.applicationID"
              cols="6"
              md="6"
              lg="4"
              xl="3"
              class="p-2"
            >
              <b-card
                no-body
                overlay
                class="app h-100"
                @mouseover="hovered = app.applicationID"
                @mouseleave="hovered = undefined"
              >
                <div class="app-icon d-flex align-items-center justify-content-center">
                  <b-img
                    class="thumbnail"
                    :src="logoUrl(app)"
                    alt=""
                  />
                </div>

                <h6 class="app-name text-center mb-0">
                  {{ app.unify.name || app.name }}
                </h6>

                <b-link
                  :data-test-id="app.name"
                  :disabled="!app.enabled"
                  :href="app.unify.url"
                  :target="openAppInNewTab(app.unify.url)"
                  :style="[{ cursor: `${app.enabled ? 'pointer': canCreateApplication ? 'grab' : 'default'}` }]"
                  class="stretched-link"
                />
              </b-card>
            </b-col>
          </transition-group>
        </draggable>

        <div
          v-else
          class="d-flex justify-content-center align-items-center mt-5 w-100"
        >
          <h4 data-test-id="heading-no-apps">
            {{ query ? $t('no-applications-found') : $t('no-applications') }}
          </h4>
        </div>
      </b-container>
    </div>
  </div>
</template>
<script>
import { mapGetters, mapActions } from 'vuex'
import Draggable from 'vuedraggable'
import { url, components } from '@cortezaproject/corteza-vue'
const { CInputSearch } = components

export default {
  i18nOptions: {
    namespaces: 'layout',
  },

  components: {
    CInputSearch,
    Draggable,
  },

  props: {
    logo: {
      type: String,
      default: () => '',
    },
  },

  data () {
    return {
      query: '',

      appList: [],

      canCreateApplication: false,
      canPin: false,

      hovered: undefined,

      isMobileResolution: false,

      steps: [
        { name: 'app-list', dynamic: false },
        { name: 'low-code', dynamic: true },
        { name: 'crm', dynamic: true },
        { name: 'reporter', dynamic: true },
        { name: 'workflow', dynamic: true },
        { name: 'profile', dynamic: false },
      ],

    }
  },

  computed: {
    ...mapGetters({
      apps: 'applications/unifyOnly',
    }),

    filteredApps () {
      const query = (this.query || '').toUpperCase()
      return this.query
        ? this.appList.filter(({ name }) => (name.toUpperCase()).includes(query))
        : this.appList
    },
  },

  watch: {
    apps: {
      immediate: true,
      handler (apps) {
        this.appList = apps
      },
    },
  },

  created () {
    this.fetchEffective()
    if (window.innerWidth < 576) {
      this.isMobileResolution = true
    }
  },

  methods: {
    ...mapActions({
      reorderApp: 'applications/reorder',
      pinApp: 'applications/pin',
      unpinApp: 'applications/unpin',
    }),

    fetchEffective () {
      this.$SystemAPI.permissionsEffective({ resource: 'application' })
        .then(p => {
          this.canCreateApplication = p.find(per => per.operation === 'application.create').allow || false
          // this.canPin = p.find(({ resource, operation, allow }) => resource === 'system' && operation === 'application.flag.self').allow
        })
    },

    handlePin (pin = true, applicationID) {
      if (pin) {
        this.unpinApp({ applicationID, ownedBy: this.$auth.user.userID })
      } else {
        this.pinApp({ applicationID, ownedBy: this.$auth.user.userID })
      }
    },

    async onDrop () {
      const applicationIDs = this.appList.map(({ applicationID }) => applicationID)
      await this.reorderApp(applicationIDs)
    },

    logoUrl (app) {
      if (!app.unify.logo) {
        return 'applications/default-app.png'
      }

      const apiSystem = '/api/system'
      const apiBaseUrl = (new URL(url.Make({ url: this.$SystemAPI.baseURL }))).toString()

      // Properly handle uploaded logos
      // but cut away only /api/system (without any potential base-url prefix)
      if (app.unify.logo.startsWith(apiSystem)) {
        // remove path from the URL
        return apiBaseUrl.substring(0, apiBaseUrl.length - apiSystem.length) + app.unify.logo
      }

      // Provisioned app logos
      return app.unify.logo
    },

    openAppInNewTab (route) {
      return !route.includes('jitsi') ? '' : '_blank'
    },
  },
}
</script>
<style lang="scss" scoped>
.app-selector {
  .logo {
    max-height: 20vh;
    max-width: 500px;
    width: auto;
  }

  @media only screen and (max-width: 576px) {
    .logo {
      max-width: 100%;
    }
  }

  .search {
    max-width: 600px;
  }

  .app {
    border-radius: 4px !important;
    min-height: 11rem;
    padding: 1.75rem 1rem 1.5rem;
    justify-content: space-between;
    transition: border-color 0.2s ease;
    box-shadow: none;
    top: 0;

    .app-icon {
      height: 72px;
      margin-bottom: 1.25rem;
    }

    .thumbnail {
      width: 64px;
      height: 64px;
      max-width: 64px;
      object-fit: contain;
    }

    .app-name {
      font-family: 'Plex-Mono-Medium', monospace;
      font-size: 13px;
      letter-spacing: .18em;
      text-transform: uppercase;
      line-height: 1.5;
    }

    &:hover {
      border-color: var(--primary) !important;

      .app-name {
        color: var(--primary);
      }
    }
  }

  @media only screen and (max-width: 576px) {
    .app {
      min-height: 9rem;
      padding: 1.25rem 1rem;

      .app-icon {
        height: 48px;
        margin-bottom: .75rem;
      }

      .thumbnail {
        width: 44px;
        height: 44px;
      }

      .app-name {
        font-size: 11px;
        letter-spacing: .14em;
      }
    }
  }

  .star {
    position: absolute;
    top: .2rem;
    left: .2rem;
    padding: 0;
    margin: 0;
    background-color: transparent;
    border: none;
    .star-icon {
      fill: var(--warning);
      width: 1.2rem;
      height: 1.2rem;
    }
  }

  .apps-leave-active {
    position: absolute;
    transition: opacity 0.25s ease;
  }
  .apps-enter, .apps-leave-to {
    opacity: 0;
  }

  .apps-move {
    transition: transform 0.25s ease;
  }
}
</style>
