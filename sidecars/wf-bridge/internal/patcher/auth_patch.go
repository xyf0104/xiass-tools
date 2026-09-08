package patcher

const (
	// Antigravity can have a fully usable local OAuth session while the account
	// eligibility response still drives the title bar into loginError. In that
	// case the original branch has already validated Cloud Code, account
	// settings, and the local OAuth token. Continue through refreshUserStatus so
	// the real local session becomes signedIn instead of launching OAuth again.
	authEligibilityOriginal = `if(s){await this.y.onboardUser("free-tier",t),await this.refreshUserStatus(t);const u=Ufe(t);this.F.pushUpdate(u),this.t.send({type:"AUTH_SUCCESS",tokenInfo:t})}else this.t.send({type:"SET_INELIGIBLE",message:a,verificationUrl:i});this.h.fire({settings:o,userTier:{description:r.paidTier?.description||""},...s?{}:{errorType:"ineligible",reason:a,verificationUrl:i}})`
	authEligibilityPatched  = `if(s){await this.y.onboardUser("free-tier",t),await this.refreshUserStatus(t);const u=Ufe(t);this.F.pushUpdate(u),this.t.send({type:"AUTH_SUCCESS",tokenInfo:t}),this.h.fire({settings:o,userTier:{description:r.paidTier?.description||""}})}else{const{settings:u,userTier:d}=await this.refreshUserStatus(t),f=Ufe(t);this.F.pushUpdate(f),this.t.send({type:"AUTH_SUCCESS",tokenInfo:t}),this.h.fire({settings:u,userTier:d})}`
)
