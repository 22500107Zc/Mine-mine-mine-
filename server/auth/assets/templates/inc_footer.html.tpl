		</main>
		{{ template "inc_toasts.html.tpl" .alerts }}
		<footer class="d-flex flex-wrap align-items-end justify-content-center text-white py-4 small">
				<span class="mr-2"><strong>St.Cloud~OS</strong> &middot; &copy; Culp Industries</span>
			<a data-test-id="link-terms" href="/legal/terms" class="text-white ml-3">Terms</a>
			<a data-test-id="link-privacy" href="/legal/privacy" class="text-white ml-3">Privacy</a>
			<a data-test-id="link-aup" href="/legal/acceptable-use" class="text-white ml-3">Acceptable Use</a>
			<a data-test-id="link-billing" href="/legal/billing" class="text-white ml-3">Billing</a>
			<a data-test-id="link-cancellation" href="/legal/cancellation" class="text-white ml-3">Cancellation</a>
			<a data-test-id="link-refunds" href="/legal/refunds" class="text-white ml-3">Refunds</a>
			<a data-test-id="link-oss" href="/legal/open-source" class="text-white ml-3">Open Source Notices</a>
			<a data-test-id="link-support" href="/support" class="text-white ml-3">Support</a>
		</footer>
	</body>
	<script src="https://code.jquery.com/jquery-3.5.1.slim.min.js" integrity="sha384-DfXdz2htPH0lsSSs5nCTpuj/zy4C+OGpamoFVy38MVBnE+IbbVYUew+OrCXaRkfj" crossorigin="anonymous"></script>
	<script src="https://cdn.jsdelivr.net/npm/bootstrap@4.6.0/dist/js/bootstrap.bundle.min.js" integrity="sha384-Piv4xVNRyMGpqkS2by6br4gNJ7DXjqk09RmUpJ8jgGtD7zP9yug3goQfGII0yAns" crossorigin="anonymous"></script>
	<script src="https://cdnjs.cloudflare.com/ajax/libs/jquery.mask/1.14.16/jquery.mask.js" integrity="sha512-0XDfGxFliYJPFrideYOoxdgNIvrwGTLnmK20xZbCAvPfLGQMzHUsaqZK8ZoH+luXGRxTrS46+Aq400nCnAT0/w==" crossorigin="anonymous"></script>
	<script src="{{ links.AuthAssets }}/script.js?{{ buildtime }}"></script>
</html>
