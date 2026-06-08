import type { ComponentType, SVGProps } from 'react';
import { STRINGS } from '../../i18n';
import {
  OverviewIcon,
  DesignIcon,
  FleetIcon,
  DeployIcon,
  SecurityIcon,
  SettingsIcon,
} from './icons';

// Single source of truth for the sidebar taxonomy, shared by the Sidebar (and,
// from P2 onward, the Topbar breadcrumb). P2 augments each item with a `path`
// when sections become real routes; P4 adds per-mode visibility.
export type NavKey = 'overview' | 'design' | 'fleet' | 'deploy' | 'security' | 'settings';

export interface NavItem {
  key: NavKey;
  /** Route path this item links to (P2). */
  path: string;
  label: readonly [string, string];
  Icon: ComponentType<SVGProps<SVGSVGElement>>;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { key: 'overview', path: '/overview', label: STRINGS.navOverview, Icon: OverviewIcon },
  { key: 'design', path: '/design', label: STRINGS.navDesign, Icon: DesignIcon },
  { key: 'fleet', path: '/fleet', label: STRINGS.navFleet, Icon: FleetIcon },
  { key: 'deploy', path: '/deploy', label: STRINGS.navDeploy, Icon: DeployIcon },
  { key: 'security', path: '/security', label: STRINGS.navSecurity, Icon: SecurityIcon },
  { key: 'settings', path: '/settings', label: STRINGS.navSettings, Icon: SettingsIcon },
];

/** Match a pathname to its nav item (exact or as a path prefix for nested routes). */
export function activeNavItem(pathname: string): NavItem | undefined {
  return NAV_ITEMS.find(
    (item) => pathname === item.path || pathname.startsWith(`${item.path}/`),
  );
}

