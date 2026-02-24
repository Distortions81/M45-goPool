package main

import (
	"sort"
	"strings"
	"time"
)

// baseTemplateData constructs a lightweight StatusData view for HTML templates
// that only need static configuration/branding fields. It intentionally avoids
// hitting the expensive statusData/metrics paths so that HTML pages rely on
// cached JSON endpoints for dynamic data.
func (s *StatusServer) baseTemplateData(start time.Time) StatusData {
	brandName := strings.TrimSpace(s.Config().StatusBrandName)
	if brandName == "" {
		brandName = "Solo Pool"
	}
	brandDomain := strings.TrimSpace(s.Config().StatusBrandDomain)

	bt := strings.TrimSpace(buildTime)
	if bt == "" {
		bt = "(dev build)"
	}
	bv := strings.TrimSpace(buildVersion)
	if bv == "" {
		bv = "(dev)"
	}

	displayPayout := shortDisplayID(s.Config().PayoutAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayDonation := shortDisplayID(s.Config().OperatorDonationAddress, payoutAddrPrefix, payoutAddrSuffix)
	displayCoinbase := shortDisplayID(s.Config().CoinbaseMsg, coinbaseMsgPrefix, coinbaseMsgSuffix)
	discordNotificationsEnabled := discordConfigured(s.Config())

	clerkPK := strings.TrimSpace(s.Config().ClerkPublishableKey)
	clerkJSURL := ""
	if clerkPK != "" {
		clerkJSHost := strings.TrimRight(strings.TrimSpace(s.Config().ClerkFrontendAPIURL), "/")
		if clerkJSHost == "" {
			clerkJSHost = strings.TrimRight(strings.TrimSpace(s.Config().ClerkIssuerURL), "/")
		}
		if clerkJSHost == "" {
			clerkJSHost = "https://clerk.clerk.com"
		}
		clerkJSURL = clerkJSHost + "/npm/@clerk/clerk-js@5/dist/clerk.browser.js"
	}

	var warnings []string
	if s.Config().PoolFeePercent > 10 {
		warnings = append(warnings, "Pool fee is configured above 10%. Verify this is intentional and clearly disclosed to miners.")
	}
	if !s.Config().DisableConnectRateLimits && s.Config().MaxAcceptsPerSecond == 0 && s.Config().MaxConns == 0 {
		warnings = append(warnings, "No connection rate limit and no max connection cap are configured. This can make the pool vulnerable to connection floods or accidental overload.")
	}
	if s != nil && !s.start.IsZero() && time.Since(s.start) >= stratumStartupGrace {
		if h := stratumHealthStatus(s.jobMgr, time.Now()); !h.Healthy {
			msg := "Node updates degraded: " + h.Reason
			if strings.TrimSpace(h.Detail) != "" {
				msg += " (" + strings.TrimSpace(h.Detail) + ")"
			}
			warnings = append(warnings, msg)
		}
	}

	bannedMinerTypes := make([]string, 0, len(s.Config().BannedMinerTypes))
	seenBannedMinerTypes := make(map[string]struct{}, len(s.Config().BannedMinerTypes))
	for _, banned := range s.Config().BannedMinerTypes {
		trimmed := strings.TrimSpace(banned)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seenBannedMinerTypes[key]; ok {
			continue
		}
		seenBannedMinerTypes[key] = struct{}{}
		bannedMinerTypes = append(bannedMinerTypes, trimmed)
	}
	sort.Strings(bannedMinerTypes)

	targetSharesPerMin := s.Config().TargetSharesPerMin
	if targetSharesPerMin <= 0 {
		targetSharesPerMin = defaultVarDiffTargetSharesPerMin
	}
	if targetSharesPerMin <= 0 {
		targetSharesPerMin = defaultVarDiffTargetSharesPerMin
	}
	minHashrateForTarget := 0.0
	maxHashrateForTarget := 0.0
	if s.Config().MinDifficulty > 0 {
		minHashrateForTarget = (s.Config().MinDifficulty * hashPerShare * targetSharesPerMin) / 60.0
	}
	if s.Config().MaxDifficulty > 0 {
		maxHashrateForTarget = (s.Config().MaxDifficulty * hashPerShare * targetSharesPerMin) / 60.0
	}

	stratumPassword := ""
	if s.Config().StratumPasswordEnabled && s.Config().StratumPasswordPublic {
		stratumPassword = s.Config().StratumPassword
	}

	return StatusData{
		ListenAddr:                      s.Config().ListenAddr,
		StratumTLSListen:                s.Config().StratumTLSListen,
		StratumV2Listen:                 s.Config().StratumV2Listen,
		StratumPasswordEnabled:          s.Config().StratumPasswordEnabled,
		StratumPasswordPublic:           s.Config().StratumPasswordPublic,
		StratumPassword:                 stratumPassword,
		BrandName:                       brandName,
		BrandDomain:                     brandDomain,
		ClerkPublishableKey:             clerkPK,
		ClerkJSURL:                      clerkJSURL,
		Tagline:                         s.Config().StatusTagline,
		ConnectMinerTitleExtra:          strings.TrimSpace(s.Config().StatusConnectMinerTitleExtra),
		ConnectMinerTitleExtraURL:       strings.TrimSpace(s.Config().StatusConnectMinerTitleExtraURL),
		ServerLocation:                  s.Config().ServerLocation,
		FiatCurrency:                    s.Config().FiatCurrency,
		PoolDonationAddress:             s.Config().PoolDonationAddress,
		DiscordURL:                      s.Config().DiscordURL,
		DiscordNotificationsEnabled:     discordNotificationsEnabled,
		GitHubURL:                       s.Config().GitHubURL,
		MempoolAddressURL:               s.Config().MempoolAddressURL,
		NodeRPCURL:                      s.Config().RPCURL,
		NodeZMQAddr:                     formatNodeZMQAddr(s.Config()),
		PayoutAddress:                   s.Config().PayoutAddress,
		PoolFeePercent:                  s.Config().PoolFeePercent,
		OperatorDonationPercent:         s.Config().OperatorDonationPercent,
		OperatorDonationAddress:         s.Config().OperatorDonationAddress,
		OperatorDonationName:            s.Config().OperatorDonationName,
		OperatorDonationURL:             s.Config().OperatorDonationURL,
		CoinbaseMessage:                 s.Config().CoinbaseMsg,
		PoolEntropy:                     s.Config().PoolEntropy,
		HashrateGraphTitle:              "Pool Hashrate",
		HashrateGraphID:                 "hashrateChart",
		DisplayPayoutAddress:            displayPayout,
		DisplayOperatorDonationAddress:  displayDonation,
		DisplayCoinbaseMessage:          displayCoinbase,
		PoolSoftware:                    poolSoftwareName,
		BuildVersion:                    bv,
		BuildTime:                       bt,
		MaxConns:                        s.Config().MaxConns,
		MaxAcceptsPerSecond:             s.Config().MaxAcceptsPerSecond,
		MaxAcceptBurst:                  s.Config().MaxAcceptBurst,
		MinDifficulty:                   s.Config().MinDifficulty,
		MaxDifficulty:                   s.Config().MaxDifficulty,
		LockSuggestedDifficulty:         s.Config().LockSuggestedDifficulty,
		BannedMinerTypes:                bannedMinerTypes,
		TargetSharesPerMin:              targetSharesPerMin,
		MinHashrateForTarget:            minHashrateForTarget,
		MaxHashrateForTarget:            maxHashrateForTarget,
		HashrateEMATauSeconds:           s.Config().HashrateEMATauSeconds,
		HashrateCumulativeEnabled:       s.Config().HashrateCumulativeEnabled,
		HashrateRecentCumulativeEnabled: s.Config().HashrateRecentCumulativeEnabled,
		ShareNTimeMaxForwardSeconds:     s.Config().ShareNTimeMaxForwardSeconds,
		RenderDuration:                  time.Since(start),
		Warnings:                        warnings,
		NodePeerCleanupEnabled:          s.Config().PeerCleanupEnabled,
		NodePeerCleanupMaxPingMs:        s.Config().PeerCleanupMaxPingMs,
		NodePeerCleanupMinPeers:         s.Config().PeerCleanupMinPeers,
	}
}
